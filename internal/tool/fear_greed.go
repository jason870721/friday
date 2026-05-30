package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/johnny1110/evva/pkg/tools"
)

// FearGreedIndexToolName is the wire name the LLM sees.
const FearGreedIndexToolName tools.ToolName = "fear_greed_index"

const fearGreedDescription = `Get the current Crypto Fear & Greed Index (0-100).

A market-wide sentiment gauge aggregated from volatility, momentum, social
media, surveys, dominance, and search trends:
- 0-24   Extreme Fear   — market is fearful; often a contrarian LONG signal.
- 25-44  Fear
- 45-55  Neutral
- 56-74  Greed
- 75-100 Extreme Greed   — market is euphoric; caution on fresh LONGs.

Use it as an EXTRA decision dimension on top of price action — e.g. fade
extreme greed, lean into extreme fear — never as the sole reason to trade.`

const fearGreedSchema = `{
	"type": "object",
	"additionalProperties": false,
	"properties": {}
}`

// fearGreedURL is the public alternative.me endpoint. A package var so
// tests can point it at an httptest server.
var fearGreedURL = "https://api.alternative.me/fng/?limit=1"

// fearGreedClient is a dedicated HTTP client — the Fear & Greed API is
// unrelated to Binance, so it doesn't share the binance client.
var fearGreedClient = &http.Client{Timeout: 10 * time.Second}

type FearGreedIndexTool struct{}

func NewFearGreedIndex() *FearGreedIndexTool { return &FearGreedIndexTool{} }

func (FearGreedIndexTool) Name() string            { return string(FearGreedIndexToolName) }
func (FearGreedIndexTool) Description() string     { return fearGreedDescription }
func (FearGreedIndexTool) Schema() json.RawMessage { return json.RawMessage(fearGreedSchema) }

// fearGreedResponse is the relevant subset of the alternative.me payload.
type fearGreedResponse struct {
	Data []struct {
		Value          string `json:"value"`
		Classification string `json:"value_classification"`
		Timestamp      string `json:"timestamp"`
	} `json:"data"`
}

// parseFearGreed turns the raw API body into a human-readable line. Split
// out from the HTTP call so it can be unit-tested without the network.
func parseFearGreed(body []byte) (string, error) {
	var r fearGreedResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	if len(r.Data) == 0 {
		return "", fmt.Errorf("no data in response")
	}
	d := r.Data[0]
	line := fmt.Sprintf("Crypto Fear & Greed Index: %s (%s)", d.Value, d.Classification)
	if ts := d.Timestamp; ts != "" {
		if secs, err := time.ParseDuration(ts + "s"); err == nil {
			t := time.Unix(int64(secs.Seconds()), 0).UTC()
			line += fmt.Sprintf(", as of %s", t.Format("2006-01-02 15:04 MST"))
		}
	}
	return line, nil
}

func (FearGreedIndexTool) Execute(ctx context.Context, logger *slog.Logger, _ json.RawMessage) (tools.Result, error) {
	logger.Debug("fear_greed_index.dispatch")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fearGreedURL, nil)
	if err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("fear_greed_index: build request: %v", err)}, nil
	}
	resp, err := fearGreedClient.Do(req)
	if err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("fear_greed_index: %v", err)}, nil
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("fear_greed_index: read body: %v", err)}, nil
	}
	if resp.StatusCode >= 400 {
		return tools.Result{IsError: true, Content: fmt.Sprintf("fear_greed_index: HTTP %d: %s", resp.StatusCode, string(body))}, nil
	}

	line, err := parseFearGreed(body)
	if err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("fear_greed_index: %v", err)}, nil
	}
	return tools.Result{Content: line}, nil
}

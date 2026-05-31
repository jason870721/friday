// Package binance provides a minimal Binance Futures REST client. Only the
// endpoints needed by friday's trading tools are implemented: premium index
// (mark price), klines, market order, balance, and position.
//
// All signed requests carry an HMAC SHA256 signature derived from the secret
// key. Unsigned endpoints (market data) skip the signature step.
package binance

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Client is a Binance Futures REST client scoped to one base URL and one
// API key/secret pair. Zero value is unusable — construct via New.
type Client struct {
	baseURL    string
	apiKey     string
	secretKey  string
	httpClient *http.Client
}

// New returns a Client that talks to baseURL with the given credentials.
// baseURL is typically https://testnet.binancefuture.com or
// https://fapi.binance.com.
func New(baseURL, apiKey, secretKey string) *Client {
	return &Client{
		baseURL:    baseURL,
		apiKey:     apiKey,
		secretKey:  secretKey,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

// get performs an unsigned GET request.
func (c *Client) get(ctx context.Context, path string, params url.Values, target any) error {
	return c.do(ctx, http.MethodGet, path, params, false, target)
}

// getSigned performs a signed GET request (adds timestamp + signature).
func (c *Client) getSigned(ctx context.Context, path string, params url.Values, target any) error {
	return c.do(ctx, http.MethodGet, path, params, true, target)
}

// postSigned performs a signed POST request.
func (c *Client) postSigned(ctx context.Context, path string, params url.Values, target any) error {
	return c.do(ctx, http.MethodPost, path, params, true, target)
}

func (c *Client) do(ctx context.Context, method, path string, params url.Values, signed bool, target any) error {
	if params == nil {
		params = url.Values{}
	}
	if signed {
		params.Set("timestamp", strconv.FormatInt(time.Now().UnixMilli(), 10))
		params.Set("recvWindow", "5000")
		sig := sign(params, c.secretKey)
		params.Set("signature", sig)
	}

	u := c.baseURL + path
	req, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return fmt.Errorf("binance: new request: %w", err)
	}
	req.Header.Set("X-MBX-APIKEY", c.apiKey)
	if method == http.MethodGet {
		req.URL.RawQuery = params.Encode()
	} else {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Body = io.NopCloser(stringReader(params.Encode()))
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("binance: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("binance: read body: %w", err)
	}

	if resp.StatusCode >= 400 {
		return fmt.Errorf("binance: %s %s returned %d: %s", method, path, resp.StatusCode, string(body))
	}

	// Check for Binance error envelope (always HTTP 200 with an error code).
	// Genuine API errors use NEGATIVE codes (e.g. -4411, -1121); some action
	// endpoints — notably the TradFi-Perps agreement — return code 200 on
	// SUCCESS, so that is not an error.
	var errResp apiError
	if err := json.Unmarshal(body, &errResp); err == nil && errResp.Code != 0 && errResp.Code != 200 {
		return fmt.Errorf("binance: %s %s: [%d] %s", method, path, errResp.Code, errResp.Msg)
	}

	if target != nil {
		if err := json.Unmarshal(body, target); err != nil {
			return fmt.Errorf("binance: decode response: %w\nbody: %s", err, string(body))
		}
	}
	return nil
}

type apiError struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
}

func (e *apiError) Error() string {
	return fmt.Sprintf("[%d] %s", e.Code, e.Msg)
}

// stringReader wraps a string as io.Reader without allocation.
type stringReader string

func (s stringReader) Read(p []byte) (int, error) {
	n := copy(p, s)
	return n, io.EOF
}

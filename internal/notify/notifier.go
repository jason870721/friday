// Package notify is friday's external-notification layer (PRD-021 §3). Friday
// runs indefinitely in a terminal; the operator can't watch the TUI 24/7. These
// notifiers push significant events — circuit-breaker trips, large PnL closes,
// session start/stop — to Discord and/or Telegram so a HALTED breaker or a big
// move is seen within seconds rather than hours later.
//
// Only SIGNIFICANT events are sent (not every round), so the providers' rate
// limits (Discord ~30/min, Telegram ~20/min) are never approached.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Notifier delivers a short title+body to an external channel. Implementations
// must be safe for concurrent use and should never block longer than a few
// seconds (they run inline with the trading loop). A nil Notifier is a valid
// no-op — callers guard with `if n != nil`.
type Notifier interface {
	Notify(title, body string) error
}

// httpClient is shared by the providers — a short timeout so a slow webhook
// can't stall the caller.
var httpClient = &http.Client{Timeout: 10 * time.Second}

// --- Discord ---

// DiscordNotifier posts to a Discord channel webhook. Plain text only (Discord
// auto-links URLs); the body is truncated to Discord's 2000-char content limit.
type DiscordNotifier struct {
	WebhookURL string
}

// Notify implements Notifier.
func (d DiscordNotifier) Notify(title, body string) error {
	content := truncate(strings.TrimSpace(title+"\n"+body), 2000)
	payload, _ := json.Marshal(map[string]string{"content": content})
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, d.WebhookURL, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("discord: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	return doNotify(req, "discord")
}

// --- Telegram ---

// TelegramNotifier posts to the Telegram Bot sendMessage API.
type TelegramNotifier struct {
	BotToken string
	ChatID   string
}

// Notify implements Notifier.
func (t TelegramNotifier) Notify(title, body string) error {
	text := truncate(strings.TrimSpace(title+"\n"+body), 4096) // Telegram message limit
	endpoint := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", t.BotToken)
	form := url.Values{"chat_id": {t.ChatID}, "text": {text}}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("telegram: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return doNotify(req, "telegram")
}

// doNotify sends req and treats any non-2xx response as an error.
func doNotify(req *http.Request, provider string) error {
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", provider, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s: webhook returned %d", provider, resp.StatusCode)
	}
	return nil
}

// --- fan-out ---

// MultiNotifier fans a notification out to several notifiers, collecting (but
// not short-circuiting on) errors so one dead webhook can't suppress the others.
type MultiNotifier struct {
	Notifiers []Notifier
}

// Notify implements Notifier.
func (m MultiNotifier) Notify(title, body string) error {
	var errs []string
	for _, n := range m.Notifiers {
		if n == nil {
			continue
		}
		if err := n.Notify(title, body); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("notify: %s", strings.Join(errs, "; "))
	}
	return nil
}

// NewFromEnv builds a Notifier from the configured providers, or returns nil
// when NONE are configured (the no-op case — callers guard with `if n != nil`).
// Recognised env:
//
//	FRIDAY_DISCORD_WEBHOOK_URL
//	FRIDAY_TELEGRAM_BOT_TOKEN + FRIDAY_TELEGRAM_CHAT_ID
func NewFromEnv() Notifier {
	var ns []Notifier
	if u := strings.TrimSpace(os.Getenv("FRIDAY_DISCORD_WEBHOOK_URL")); u != "" {
		ns = append(ns, DiscordNotifier{WebhookURL: u})
	}
	tok := strings.TrimSpace(os.Getenv("FRIDAY_TELEGRAM_BOT_TOKEN"))
	chat := strings.TrimSpace(os.Getenv("FRIDAY_TELEGRAM_CHAT_ID"))
	if tok != "" && chat != "" {
		ns = append(ns, TelegramNotifier{BotToken: tok, ChatID: chat})
	}
	switch len(ns) {
	case 0:
		return nil
	case 1:
		return ns[0]
	default:
		return MultiNotifier{Notifiers: ns}
	}
}

// truncate caps s at n runes, appending an ellipsis when it had to cut.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}

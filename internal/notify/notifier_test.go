package notify

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestDiscordNotifier_Sends(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = string(b)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	n := DiscordNotifier{WebhookURL: srv.URL}
	if err := n.Notify("title", "body"); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if !strings.Contains(got, "title") || !strings.Contains(got, "body") {
		t.Errorf("payload missing title/body: %s", got)
	}
}

func TestTelegramNotifier_Sends(t *testing.T) {
	var hit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		b, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(b), "chat_id=123") {
			t.Errorf("missing chat_id: %s", b)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// Point the bot endpoint at the test server by overriding the client's
	// transport to rewrite the host.
	old := httpClient
	httpClient = &http.Client{Transport: rewriteHost{target: srv.URL}}
	defer func() { httpClient = old }()

	n := TelegramNotifier{BotToken: "TOKEN", ChatID: "123"}
	if err := n.Notify("t", "b"); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if !hit {
		t.Error("telegram endpoint was not called")
	}
}

// rewriteHost sends every request to target instead of its real host — lets the
// Telegram test hit a local server without touching api.telegram.org.
type rewriteHost struct{ target string }

func (rh rewriteHost) RoundTrip(req *http.Request) (*http.Response, error) {
	u, _ := http.NewRequest(req.Method, rh.target, req.Body)
	u.Header = req.Header
	return http.DefaultTransport.RoundTrip(u)
}

func TestMultiNotifier_FansOut(t *testing.T) {
	var mu sync.Mutex
	count := 0
	rec := recorder{fn: func() { mu.Lock(); count++; mu.Unlock() }}
	m := MultiNotifier{Notifiers: []Notifier{rec, nil, rec}}
	if err := m.Notify("a", "b"); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if count != 2 {
		t.Errorf("fan-out hit %d notifiers; want 2 (nil skipped)", count)
	}
}

type recorder struct{ fn func() }

func (r recorder) Notify(title, body string) error { r.fn(); return nil }

func TestNewFromEnv(t *testing.T) {
	t.Setenv("FRIDAY_DISCORD_WEBHOOK_URL", "")
	t.Setenv("FRIDAY_TELEGRAM_BOT_TOKEN", "")
	t.Setenv("FRIDAY_TELEGRAM_CHAT_ID", "")
	if n := NewFromEnv(); n != nil {
		t.Errorf("no config → nil notifier, got %T", n)
	}
	t.Setenv("FRIDAY_DISCORD_WEBHOOK_URL", "https://example.com/hook")
	if _, ok := NewFromEnv().(DiscordNotifier); !ok {
		t.Errorf("discord-only → DiscordNotifier")
	}
	t.Setenv("FRIDAY_TELEGRAM_BOT_TOKEN", "tok")
	t.Setenv("FRIDAY_TELEGRAM_CHAT_ID", "42")
	if _, ok := NewFromEnv().(MultiNotifier); !ok {
		t.Errorf("both configured → MultiNotifier")
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("hello", 10); got != "hello" {
		t.Errorf("no truncation expected, got %q", got)
	}
	if got := truncate("hello world", 5); len([]rune(got)) != 5 {
		t.Errorf("truncate to 5 runes, got %q (%d)", got, len([]rune(got)))
	}
}

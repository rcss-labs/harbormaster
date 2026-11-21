package notify

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/store"
)

var telegramToken = "123456789:" + strings.Repeat("Ab-_", 9) // 46 characters of the BotFather alphabet

func telegramRoute() store.NotificationRoute {
	return store.NotificationRoute{Destination: DestTelegram, Config: store.NotificationConfig{ChatID: "-1001234567890", ThreadID: 5}}
}

func (h *harness) sendTelegram(event Event) Result {
	h.t.Helper()
	return h.send(telegramRoute(), Secret{BotToken: telegramToken}, event)
}

func TestTelegramSender_Delivers(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"ok":true,"result":{"message_id":1}}`)
	})
	res := h.sendTelegram(EventOpened)
	if !res.Delivered || !strings.HasPrefix(res.Note, "Delivered in ") {
		t.Fatalf("result = %+v", res)
	}
	reqs := h.recorded()
	if len(reqs) != 1 || reqs[0].method != "POST" || reqs[0].path != "/bot"+telegramToken+"/sendMessage" {
		t.Fatalf("requests = %+v", reqs)
	}
	if want, _ := TelegramBody(testMessage(EventOpened), "-1001234567890", 5); string(reqs[0].body) != string(want) {
		t.Errorf("body = %s", reqs[0].body)
	}
	if reqs[0].header.Get("X-Harbormaster-Delivery") != "42" {
		t.Error("the delivery header is missing")
	}
	h.mu.Lock()
	hosts := h.hosts
	h.mu.Unlock()
	if len(hosts) != 1 || hosts[0] != "api.telegram.org" {
		t.Errorf("the request went to %v, want the fixed Telegram host", hosts)
	}
}

func TestTelegramSender_StatusMapping(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		body      string
		delivered bool
		retryable bool
		retry     time.Duration
		note      string
	}{
		{"ok", 200, `{"ok":true,"result":{}}`, true, false, 0, "Delivered in"},
		{"200 but not ok", 200, `{"ok":false}`, false, false, 0, "Telegram returned an unexpected answer."},
		{"200 with garbage", 200, `<html>`, false, false, 0, "Telegram returned an unexpected answer."},
		{"bad request", 400, `{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`, false, false, 0, "Telegram rejected the message or the chat ID."},
		{"unauthorized", 401, `{"ok":false,"error_code":401,"description":"Unauthorized"}`, false, false, 0, "Telegram rejected the bot token."},
		{"forbidden", 403, `{"ok":false,"error_code":403,"description":"Forbidden: bot was kicked"}`, false, false, 0, "The bot cannot post to this chat. Add it to the chat first."},
		{"not found", 404, `{"ok":false,"error_code":404,"description":"Not Found"}`, false, false, 0, "Telegram rejected the bot token."},
		{"rate limited", 429, `{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 37","parameters":{"retry_after":37}}`, false, true, 37 * time.Second, "Telegram rate limit"},
		{"rate limited for long", 429, `{"ok":false,"error_code":429,"parameters":{"retry_after":4000}}`, false, true, 5 * time.Minute, "Telegram rate limit"},
		{"server error", 500, `{"ok":false}`, false, true, 0, "HTTP 500"},
		{"other status", 418, `{}`, false, false, 0, "HTTP 418"},
	}
	for _, tc := range tests {
		h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			io.WriteString(w, tc.body)
		})
		res := h.sendTelegram(EventOpened)
		if res.Delivered != tc.delivered || res.Retryable != tc.retryable || res.RetryAfter != tc.retry || !strings.HasPrefix(res.Note, tc.note) || res.StatusCode != tc.status {
			t.Errorf("%s: result = %+v", tc.name, res)
		}
		if strings.Contains(res.Note, "chat not found") || strings.Contains(res.Note, "kicked") {
			t.Errorf("%s: the description leaked into the note %q", tc.name, res.Note)
		}
	}
}

func TestTelegramSender_RetryAfterHeaderIsTheFallback(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "12")
		w.WriteHeader(429)
		io.WriteString(w, `{"ok":false,"error_code":429}`)
	})
	if res := h.sendTelegram(EventOpened); res.RetryAfter != 12*time.Second || !res.Retryable {
		t.Fatalf("result = %+v", res)
	}
}

func TestTelegramSender_TokenNeverInNoteOrLog(t *testing.T) {
	echo := func(status int) func(h *harness) {
		return func(h *harness) {
			h.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				io.WriteString(w, `{"ok":false,"description":"`+r.URL.Path+" "+telegramToken+`"}`)
			})
		}
	}
	cases := map[string]func(h *harness){
		"blocked": func(h *harness) {
			h.senders.lookup = func(context.Context, string) ([]netip.Addr, error) {
				return []netip.Addr{netip.MustParseAddr("169.254.169.254")}, nil
			}
		},
		"refused": func(h *harness) {
			h.senders.dial = func(context.Context, string, string) (net.Conn, error) {
				return nil, &net.OpError{Op: "dial", Net: "tcp", Err: io.ErrClosedPipe}
			}
		},
		"timeout": func(h *harness) {
			h.senders.Timeout = 50 * time.Millisecond
			h.senders.dial = func(ctx context.Context, _, _ string) (net.Conn, error) {
				<-ctx.Done()
				return nil, ctx.Err()
			}
		},
		"tls":    func(h *harness) { h.senders.tls = &tls.Config{ServerName: "example.com"} },
		"400":    echo(400),
		"500":    echo(500),
		"200 ok": echo(200),
	}
	for name, setup := range cases {
		h := newHarness(t, nil)
		setup(h)
		res := h.sendTelegram(EventOpened)
		for _, leak := range []string{telegramToken, "/bot", "api.telegram.org", "123456789"} {
			if strings.Contains(res.Note, leak) || strings.Contains(h.logs.String(), leak) {
				t.Errorf("%s: %q leaked into the note %q or the log %q", name, leak, res.Note, h.logs.String())
			}
		}
		if res.Delivered && name != "200 ok" {
			t.Errorf("%s: unexpectedly delivered", name)
		}
	}
}

func TestTelegramSender_DoesNotFollowRedirects(t *testing.T) {
	second := newHarness(t, nil)
	first := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, second.srv.URL+"/landing", http.StatusFound)
	})
	res := first.sendTelegram(EventOpened)
	if res.Delivered || res.Retryable || res.Note != "HTTP 302" || len(second.recorded()) != 0 {
		t.Fatalf("result = %+v, second server saw %d requests", res, len(second.recorded()))
	}
}

func TestTelegramSender_UsesTheGuard(t *testing.T) {
	h := newHarness(t, nil)
	h.senders.lookup = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("10.0.0.5")}, nil
	}
	res := h.sendTelegram(EventOpened)
	if res.Note != "The destination is not reachable from Harbormaster." || len(h.dialled) != 0 {
		t.Fatalf("result = %+v dialled = %v", res, h.dialled)
	}
}

func TestTelegramSender_FakeBaseIsUsedInFakeMode(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"ok":true}`) })
	h.senders.FakeBase = "https://hm.test/fake-notify"
	h.senders.TelegramBase = "https://hm.test/fake-notify/telegram"
	if res := h.sendTelegram(EventOpened); !res.Delivered {
		t.Fatalf("result = %+v", res)
	}
	if got := h.recorded()[0].path; got != "/fake-notify/telegram/bot"+telegramToken+"/sendMessage" {
		t.Errorf("path = %q", got)
	}
}

func TestValidateDestination_Telegram(t *testing.T) {
	s := &Senders{}
	tests := []struct {
		name    string
		cfg     store.NotificationConfig
		token   string
		wantErr string
	}{
		{"numeric chat", store.NotificationConfig{ChatID: "-1001234567890"}, telegramToken, ""},
		{"positive chat", store.NotificationConfig{ChatID: "42"}, telegramToken, ""},
		{"channel name", store.NotificationConfig{ChatID: "@ops_alerts"}, telegramToken, ""},
		{"topic", store.NotificationConfig{ChatID: "-100123", ThreadID: 2147483647}, telegramToken, ""},
		{"no token", store.NotificationConfig{ChatID: "-100123"}, "", "bot token"},
		{"short token", store.NotificationConfig{ChatID: "-100123"}, "123:abc", "bot token"},
		{"token with a space", store.NotificationConfig{ChatID: "-100123"}, "123456789:" + strings.Repeat("a", 34) + " ", "bot token"},
		{"no chat", store.NotificationConfig{}, telegramToken, "chat ID"},
		{"chat with text", store.NotificationConfig{ChatID: "ops"}, telegramToken, "chat ID"},
		{"channel too short", store.NotificationConfig{ChatID: "@abc"}, telegramToken, "chat ID"},
		{"topic too large", store.NotificationConfig{ChatID: "-100123", ThreadID: 2147483648}, telegramToken, "topic"},
		{"negative topic", store.NotificationConfig{ChatID: "-100123", ThreadID: -1}, telegramToken, "topic"},
	}
	for _, tc := range tests {
		err := s.ValidateDestination(DestTelegram, tc.cfg, Secret{BotToken: tc.token})
		switch {
		case tc.wantErr == "" && err != nil:
			t.Errorf("%s: unexpected error %v", tc.name, err)
		case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
			t.Errorf("%s: error = %v, want one containing %q", tc.name, err, tc.wantErr)
		}
		if err != nil && tc.token != "" && strings.Contains(err.Error(), tc.token) {
			t.Errorf("%s: the error echoes the token", tc.name)
		}
	}
}

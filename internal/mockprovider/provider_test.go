package mockprovider

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"campaign-composer/internal/webhook"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func start(t *testing.T, cfg Config) (*Provider, string) {
	t.Helper()
	p := New(cfg, quiet)
	ts := httptest.NewServer(p.Handler())
	t.Cleanup(func() { ts.Close(); p.Close() })
	return p, ts.URL
}

func send(t *testing.T, base, channel, key, body string) (int, map[string]any, http.Header) {
	t.Helper()
	req, _ := http.NewRequest("POST", base+"/v1/messages/"+channel, strings.NewReader(body))
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out, resp.Header
}

const smsBody = `{"to":"+905321234567","body":"Merhaba","reference":"delivery-1"}`

func TestAcceptAndIdempotentReplay(t *testing.T) {
	p, url := start(t, Config{})
	status, first, _ := send(t, url, "sms", "k1", smsBody)
	if status != 202 || first["message_id"] == "" {
		t.Fatalf("first send: %d %v", status, first)
	}
	status, again, _ := send(t, url, "sms", "k1", smsBody)
	if status != 200 || again["message_id"] != first["message_id"] {
		t.Fatalf("replay: %d %v, want the first acceptance again", status, again)
	}
	if s := p.Stats(); s.Accepted != 1 || s.Replays != 1 || p.Accepted()["delivery-1"] != 1 {
		t.Fatalf("stats = %+v", s)
	}
}

func TestRejectsBadRequests(t *testing.T) {
	_, url := start(t, Config{})
	cases := []struct {
		channel, key, body, code string
		status                   int
	}{
		{"sms", "", smsBody, "missing_idempotency_key", 400},
		{"sms", "a", `{"to":"+90555","body":"x","reference":"r"}`, "invalid_recipient", 400},
		{"email", "b", `{"to":"no-at-sign.example.com","subject":"s","body":"x","reference":"r"}`, "invalid_recipient", 400},
		{"email", "c", `{"to":"a@example.com","body":"x","reference":"r"}`, "invalid_recipient", 400}, // no subject
		{"fax", "d", smsBody, "unknown_channel", 404},
	}
	for _, c := range cases {
		status, body, _ := send(t, url, c.channel, c.key, c.body)
		if status != c.status || body["error"] != c.code {
			t.Errorf("%s %s: %d %v, want %d %s", c.channel, c.body, status, body, c.status, c.code)
		}
	}
}

func TestChaos(t *testing.T) {
	_, url := start(t, Config{ErrorRate: 1})
	if status, _, _ := send(t, url, "sms", "e", smsBody); status != 500 {
		t.Errorf("error rate 1: status %d", status)
	}
	_, url = start(t, Config{RateLimitRate: 1})
	status, _, h := send(t, url, "sms", "r", smsBody)
	if status != 429 || h.Get("Retry-After") == "" {
		t.Errorf("429 rate 1: status %d, Retry-After %q", status, h.Get("Retry-After"))
	}
}

func TestDroppedResponseIsReplayed(t *testing.T) {
	p, url := start(t, Config{DropRate: 1})
	status, _, _ := send(t, url, "sms", "k", smsBody)
	if status != 502 {
		t.Fatalf("drop: status %d", status)
	}
	// The client retries with the same key: it learns the message was accepted.
	status, body, _ := send(t, url, "sms", "k", smsBody)
	if status != 200 || body["message_id"] == "" || p.Accepted()["delivery-1"] != 1 {
		t.Fatalf("retry after drop: %d %v accepted=%d", status, body, p.Accepted()["delivery-1"])
	}
}

func TestMaxRPS(t *testing.T) {
	_, url := start(t, Config{MaxRPS: 5})
	limited := 0
	for i := range 20 {
		if status, _, _ := send(t, url, "email", "k"+string(rune('a'+i)),
			`{"to":"a@example.com","subject":"s","body":"x","reference":"r`+string(rune('a'+i))+`"}`); status == 429 {
			limited++
		}
	}
	if limited < 10 {
		t.Fatalf("only %d of 20 instant requests were limited at 5 rps", limited)
	}
}

func TestSignedReceipts(t *testing.T) {
	var mu sync.Mutex
	var got []receipt
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := webhook.Verify("s3cret", r.Header.Get(webhook.SignatureHeader), r.Header.Get(webhook.TimestampHeader), body, time.Now()); err != nil {
			t.Errorf("receipt signature: %v", err)
			w.WriteHeader(401)
			return
		}
		var rc receipt
		json.Unmarshal(body, &rc)
		mu.Lock()
		got = append(got, rc)
		mu.Unlock()
	}))
	defer hook.Close()

	_, url := start(t, Config{WebhookURL: hook.URL, WebhookSecret: "s3cret", DeliveredRate: 1, WebhookDupRate: 1})
	_, acc, _ := send(t, url, "sms", "k", smsBody)

	deadline := time.Now().Add(3 * time.Second)
	for {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n == 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 { // duplicate rate 1: the same receipt twice
		t.Fatalf("got %d receipts, want 2", len(got))
	}
	if got[0].MessageID != acc["message_id"] || got[0].Reference != "delivery-1" || got[0].Status != "delivered" || got[0] != got[1] {
		t.Fatalf("receipts = %+v", got)
	}
}

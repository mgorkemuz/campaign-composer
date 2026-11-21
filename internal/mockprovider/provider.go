// Package mockprovider is a fake SMS/email provider that misbehaves on purpose,
// so the app's retry, rate-limit and idempotency logic can be tested.
//
// API (JSON):
//
//	POST /v1/messages/sms    {"to": "+905...", "body": "...", "reference": "..."}
//	POST /v1/messages/email  {"to": "a@b.c", "subject": "...", "body": "...", "reference": "..."}
//	    Idempotency-Key header required. 202 {"message_id": "..."}.
//	    A repeated key returns the first acceptance again (200), so retries never
//	    send twice.
//	GET  /stats   counters for tests and demos
//	GET  /healthz
//
// After accepting a message it POSTs a signed delivery receipt to WebhookURL:
// {"message_id", "reference", "channel", "status": "delivered"|"undelivered", "timestamp"}.
package mockprovider

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	mrand "math/rand/v2"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"campaign-composer/internal/webhook"
)

// Config sets how badly the provider behaves. Rates are probabilities (0..1).
type Config struct {
	ErrorRate     float64 // 500 Internal Server Error
	RateLimitRate float64 // 429 with Retry-After, on top of MaxRPS
	DropRate      float64 // accept the message, then answer 502 as if the response was lost
	SlowRate      float64 // answer after SlowLatency
	MaxRPS        int     // per channel; above it, 429. 0 means no limit.

	LatencyMin, LatencyMax time.Duration
	SlowLatency            time.Duration

	WebhookURL       string
	WebhookSecret    string
	DeliveredRate    float64 // share of receipts that say delivered; the rest undelivered
	WebhookDelayMin  time.Duration
	WebhookDelayMax  time.Duration
	WebhookDupRate   float64 // send the receipt twice
	WebhookEarlyRate float64 // send the receipt before answering the send request

	Seed uint64
}

type Stats struct {
	Accepted        int `json:"accepted"`
	Replays         int `json:"idempotent_replays"`
	Invalid         int `json:"rejected_invalid"`
	RateLimited     int `json:"rate_limited"`
	ServerErrors    int `json:"server_errors"`
	Dropped         int `json:"dropped_responses"`
	WebhooksSent    int `json:"webhooks_sent"`
	WebhooksFailed  int `json:"webhooks_failed"`
	DuplicateAccept int `json:"duplicate_references"` // same reference accepted under two keys: an app bug
}

type acceptance struct {
	MessageID string `json:"message_id"`
	Status    string `json:"status"`
}

type Provider struct {
	cfg    Config
	log    *slog.Logger
	client *http.Client

	mu         sync.Mutex
	rng        *mrand.Rand
	accepted   map[string]acceptance // idempotency key -> first acceptance
	references map[string]int        // reference -> acceptances (should never exceed 1)
	stats      Stats
	limiters   map[string]*rate.Limiter

	webhooks sync.WaitGroup
	ctx      context.Context
	stop     context.CancelFunc
}

func New(cfg Config, log *slog.Logger) *Provider {
	if cfg.DeliveredRate == 0 {
		cfg.DeliveredRate = 0.95
	}
	ctx, stop := context.WithCancel(context.Background())
	p := &Provider{
		cfg: cfg, log: log, client: &http.Client{Timeout: 5 * time.Second},
		rng:      mrand.New(mrand.NewPCG(cfg.Seed, cfg.Seed^0xabcdef)),
		accepted: map[string]acceptance{}, references: map[string]int{}, limiters: map[string]*rate.Limiter{},
		ctx: ctx, stop: stop,
	}
	if cfg.MaxRPS > 0 {
		for _, ch := range []string{"sms", "email"} {
			p.limiters[ch] = rate.NewLimiter(rate.Limit(cfg.MaxRPS), cfg.MaxRPS)
		}
	}
	return p
}

// Close stops sending receipts and waits for those in flight.
func (p *Provider) Close() {
	p.stop()
	p.webhooks.Wait()
}

func (p *Provider) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/messages/{channel}", p.handleSend)
	mux.HandleFunc("GET /stats", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, p.Stats()) })
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	return mux
}

func (p *Provider) Stats() Stats {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stats
}

// Accepted returns how many times each reference was accepted. Tests use it to
// prove every message was sent exactly once.
func (p *Provider) Accepted() map[string]int {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make(map[string]int, len(p.references))
	for k, v := range p.references {
		out[k] = v
	}
	return out
}

type sendRequest struct {
	To        string `json:"to"`
	Subject   string `json:"subject"`
	Body      string `json:"body"`
	Reference string `json:"reference"`
}

var (
	phoneRe = regexp.MustCompile(`^\+[1-9][0-9]{9,14}$`)
	emailRe = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)
)

func (p *Provider) handleSend(w http.ResponseWriter, r *http.Request) {
	channel := r.PathValue("channel")
	if channel != "sms" && channel != "email" {
		writeError(w, 404, "unknown_channel")
		return
	}
	p.sleep(p.latency())

	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		writeError(w, 400, "missing_idempotency_key")
		return
	}
	p.mu.Lock()
	if prev, ok := p.accepted[key]; ok {
		p.stats.Replays++
		p.mu.Unlock()
		writeJSON(w, 200, prev) // a retry of something already accepted: same answer, nothing sent
		return
	}
	p.mu.Unlock()

	var req sendRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil || req.Reference == "" || req.Body == "" {
		p.count(func(s *Stats) { s.Invalid++ })
		writeError(w, 400, "invalid_request")
		return
	}
	valid := phoneRe.MatchString(req.To)
	if channel == "email" {
		valid = emailRe.MatchString(req.To) && req.Subject != ""
	}
	if !valid {
		p.count(func(s *Stats) { s.Invalid++ })
		writeError(w, 400, "invalid_recipient")
		return
	}

	if l := p.limiters[channel]; l != nil && !l.Allow() {
		p.count(func(s *Stats) { s.RateLimited++ })
		w.Header().Set("Retry-After", "1")
		writeError(w, 429, "rate_limited")
		return
	}
	switch roll := p.roll(); {
	case roll < p.cfg.RateLimitRate:
		p.count(func(s *Stats) { s.RateLimited++ })
		w.Header().Set("Retry-After", strconv.Itoa(1+p.intN(2)))
		writeError(w, 429, "rate_limited")
		return
	case roll < p.cfg.RateLimitRate+p.cfg.ErrorRate:
		p.count(func(s *Stats) { s.ServerErrors++ })
		writeError(w, 500, "internal_error")
		return
	}

	acc := acceptance{MessageID: "msg_" + randomHex(8), Status: "accepted"}
	p.mu.Lock()
	if prev, ok := p.accepted[key]; ok { // a concurrent retry with the same key won the race
		p.stats.Replays++
		p.mu.Unlock()
		writeJSON(w, 200, prev)
		return
	}
	p.accepted[key] = acc
	p.references[req.Reference]++
	if p.references[req.Reference] > 1 {
		p.stats.DuplicateAccept++
	}
	p.stats.Accepted++
	early := p.rng.Float64() < p.cfg.WebhookEarlyRate
	drop := p.rng.Float64() < p.cfg.DropRate
	p.mu.Unlock()

	p.scheduleReceipt(acc.MessageID, req.Reference, channel, early)
	if drop {
		p.count(func(s *Stats) { s.Dropped++ })
		writeError(w, 502, "upstream_connection_reset") // accepted, but the client never learns it
		return
	}
	writeJSON(w, 202, acc)
}

type receipt struct {
	MessageID string    `json:"message_id"`
	Reference string    `json:"reference"`
	Channel   string    `json:"channel"`
	Status    string    `json:"status"`
	Timestamp time.Time `json:"timestamp"`
}

func (p *Provider) scheduleReceipt(messageID, reference, channel string, early bool) {
	if p.cfg.WebhookURL == "" {
		return
	}
	p.mu.Lock()
	status := "delivered"
	if p.rng.Float64() >= p.cfg.DeliveredRate {
		status = "undelivered"
	}
	delay := time.Duration(0)
	if !early {
		delay = p.cfg.WebhookDelayMin
		if span := p.cfg.WebhookDelayMax - p.cfg.WebhookDelayMin; span > 0 {
			delay += time.Duration(p.rng.Int64N(int64(span)))
		}
	}
	copies := 1
	if p.rng.Float64() < p.cfg.WebhookDupRate {
		copies = 2
	}
	p.mu.Unlock()

	body, _ := json.Marshal(receipt{MessageID: messageID, Reference: reference, Channel: channel, Status: status, Timestamp: time.Now().UTC()})
	p.webhooks.Add(1)
	go func() {
		defer p.webhooks.Done()
		select {
		case <-p.ctx.Done():
			return
		case <-time.After(delay):
		}
		for range copies {
			p.postReceipt(body)
		}
	}()
}

// postReceipt delivers one receipt, retrying with backoff like real providers.
func (p *Provider) postReceipt(body []byte) {
	wait := 200 * time.Millisecond
	for attempt := 1; attempt <= 4; attempt++ {
		now := time.Now()
		req, _ := http.NewRequestWithContext(p.ctx, http.MethodPost, p.cfg.WebhookURL, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(webhook.TimestampHeader, strconv.FormatInt(now.Unix(), 10))
		req.Header.Set(webhook.SignatureHeader, webhook.Sign(p.cfg.WebhookSecret, now, body))
		resp, err := p.client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode < 300 {
				p.count(func(s *Stats) { s.WebhooksSent++ })
				return
			}
		}
		select {
		case <-p.ctx.Done():
			return
		case <-time.After(wait):
		}
		wait *= 2
	}
	p.count(func(s *Stats) { s.WebhooksFailed++ })
	p.log.Warn("delivery receipt could not be delivered", "url", p.cfg.WebhookURL)
}

func (p *Provider) latency() time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.rng.Float64() < p.cfg.SlowRate {
		return p.cfg.SlowLatency
	}
	d := p.cfg.LatencyMin
	if span := p.cfg.LatencyMax - p.cfg.LatencyMin; span > 0 {
		d += time.Duration(p.rng.Int64N(int64(span)))
	}
	return d
}

func (p *Provider) sleep(d time.Duration) {
	if d > 0 {
		time.Sleep(d)
	}
}

func (p *Provider) roll() float64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.rng.Float64()
}

func (p *Provider) intN(n int) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.rng.IntN(n)
}

func (p *Provider) count(f func(*Stats)) {
	p.mu.Lock()
	f(&p.stats)
	p.mu.Unlock()
}

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code, "message": strings.ReplaceAll(code, "_", " ")})
}

// String summarises the configuration for the startup log.
func (c Config) String() string {
	return fmt.Sprintf("errors=%.0f%% 429s=%.0f%% drops=%.0f%% slow=%.0f%% max_rps=%d latency=%s-%s receipts=%s",
		c.ErrorRate*100, c.RateLimitRate*100, c.DropRate*100, c.SlowRate*100, c.MaxRPS, c.LatencyMin, c.LatencyMax, c.WebhookURL)
}

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"campaign-composer/internal/backoff"
	"campaign-composer/internal/campaign"
	"campaign-composer/internal/composer"
	"campaign-composer/internal/delivery"
	"campaign-composer/internal/events"
	"campaign-composer/internal/llm/llmtest"
	"campaign-composer/internal/metrics"
	"campaign-composer/internal/mockprovider"
	"campaign-composer/internal/queue"
	"campaign-composer/internal/seed"
	"campaign-composer/internal/testdb"
	"campaign-composer/internal/webhook"
)

const webhookSecret = "test-webhook-secret"

type sendingEnv struct {
	api      *httptest.Server
	pool     *pgxpool.Pool
	provider *mockprovider.Provider
}

// sendingServer runs the API, send workers and (optionally) the mock provider
// in-process, wired like main. With fake set, sends go to it instead.
func sendingServer(t *testing.T, chaos mockprovider.Config, fake delivery.Provider, rate float64) sendingEnv {
	return sendingServerWith(t, chaos, fake, rate, nil)
}

func sendingServerWith(t *testing.T, chaos mockprovider.Config, fake delivery.Provider, rate float64, obs delivery.Observer) sendingEnv {
	t.Helper()
	pool := testdb.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	if _, err := seed.Users(ctx, pool, seed.Options{N: 5000, Seed: 42, Now: fixedNow}); err != nil {
		t.Fatal(err)
	}
	hub := events.NewHub(pool, quiet)
	go hub.Run(ctx)
	<-hub.Ready()

	clock := func() time.Time { return fixedNow }
	svc := campaign.NewService(pool, composer.New(llmtest.New(), quiet), clock)
	svc.DeliveryMaxAttempts = 20 // chaos must never exhaust a message's attempts by bad luck
	s := New(pool, quiet, Options{Hub: hub, Now: clock, Campaigns: svc, WebhookSecret: webhookSecret})
	apiSrv := httptest.NewServer(s.Handler())

	env := sendingEnv{api: apiSrv, pool: pool}
	var provSrv *httptest.Server
	if fake == nil {
		chaos.WebhookURL = apiSrv.URL + "/webhooks/provider"
		chaos.WebhookSecret = webhookSecret
		env.provider = mockprovider.New(chaos, quiet)
		provSrv = httptest.NewServer(env.provider.Handler())
		fake = delivery.NewClient(provSrv.URL)
	}

	sender := delivery.NewSender(pool, fake, rate, quiet, obs)
	var wg sync.WaitGroup
	for _, ch := range delivery.Channels {
		w := &queue.Worker{Pool: pool, Hub: hub, Log: quiet, Kind: delivery.JobKind(ch),
			Handler: sender.Handle, OnFailed: sender.OnFailed, Concurrency: 16,
			PollEvery: 20 * time.Millisecond, Backoff: backoff.Policy{Base: 5 * time.Millisecond, Max: 50 * time.Millisecond}}
		wg.Add(1)
		go func() { defer wg.Done(); w.Run(ctx) }()
	}
	t.Cleanup(func() {
		s.CloseStreams()
		cancel()
		wg.Wait()
		if env.provider != nil {
			env.provider.Close()
			provSrv.Close()
		}
		apiSrv.Close()
	})
	return env
}

// readyCampaign makes a campaign for one city with valid messages, without the LLM.
func readyCampaign(t *testing.T, base, city string) string {
	t.Helper()
	id := createCampaign(t, &httptest.Server{URL: base})
	if status, res := call(t, "PUT", base+"/api/campaigns/"+id+"/audience",
		`{"rule":{"match":"all","conditions":[{"field":"city","op":"eq","value":"`+city+`"}]}}`); status != 200 {
		t.Fatalf("audience: %d %v", status, res)
	}
	if status, res := call(t, "PUT", base+"/api/campaigns/"+id+"/content",
		`{"language":"tr","sms":"Merhaba {{first_name}}, sana özel %30 indirim!","email_subject":"{{first_name}}, sana özel indirim","email_body":"Merhaba {{first_name}},\n\nSana özel %30 indirim var.\n\nSevgiler,\nEkip"}`); status != 200 {
		t.Fatalf("content: %d %v", status, res)
	}
	return id
}

func waitUntil(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

var (
	validPhone = regexp.MustCompile(`^\+[1-9][0-9]{9,14}$`)
	validEmail = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)
)

// TestEveryMessageSentExactlyOnceUnderChaos is the core reliability test: a
// whole campaign against a provider that fails, rate-limits, loses responses
// and sends duplicate or early receipts.
func TestEveryMessageSentExactlyOnceUnderChaos(t *testing.T) {
	m := metrics.New(nil)
	env := sendingServerWith(t, mockprovider.Config{
		ErrorRate: 0.10, RateLimitRate: 0.05, DropRate: 0.05,
		LatencyMax: 3 * time.Millisecond,
		// Receipts slower than retries (as in reality), so a lost response is
		// recovered by the idempotent replay, not by an early receipt.
		WebhookDelayMin: 100 * time.Millisecond, WebhookDelayMax: 300 * time.Millisecond,
		WebhookDupRate: 0.2, WebhookEarlyRate: 0.2,
		DeliveredRate: 0.9, Seed: 1,
	}, nil, 2000, m)
	ctx := context.Background()
	id := readyCampaign(t, env.api.URL, "İzmir")

	status, res := call(t, "POST", env.api.URL+"/api/campaigns/"+id+"/approve", "")
	if status != 200 {
		t.Fatalf("approve: %d %v", status, res)
	}
	var wantSMS, wantEmail int
	env.pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE sms_consent), count(*) FILTER (WHERE email_consent)
		FROM users WHERE city = 'İzmir'`).Scan(&wantSMS, &wantEmail)
	if int(res["sms"].(float64)) != wantSMS || int(res["email"].(float64)) != wantEmail || wantSMS == 0 {
		t.Fatalf("queued %v sms / %v email; consenting İzmir users: %d / %d", res["sms"], res["email"], wantSMS, wantEmail)
	}

	// Approving again changes nothing.
	if status, _ := call(t, "POST", env.api.URL+"/api/campaigns/"+id+"/approve", ""); status != 409 {
		t.Errorf("second approve: status %d, want 409", status)
	}

	waitUntil(t, "the campaign to complete", 60*time.Second, func() bool {
		_, c := call(t, "GET", env.api.URL+"/api/campaigns/"+id, "")
		return c["status"] == "completed"
	})
	waitUntil(t, "all receipts", 15*time.Second, func() bool {
		var pending int
		env.pool.QueryRow(ctx, `SELECT count(*) FROM deliveries WHERE campaign_id = $1 AND status = 'sent'`, id).Scan(&pending)
		return pending == 0
	})

	rows, err := env.pool.Query(ctx, `
		SELECT d.id, d.channel, d.status, d.attempts, d.last_error, u.phone, u.email
		FROM deliveries d JOIN users u ON u.id = d.user_id WHERE d.campaign_id = $1`, id)
	if err != nil {
		t.Fatal(err)
	}
	accepted := env.provider.Accepted()
	byStatus := map[string]int{}
	total := 0
	for rows.Next() {
		var (
			did                           int64
			ch, st, lastErr, phone, email string
			attempts                      int
		)
		if err := rows.Scan(&did, &ch, &st, &attempts, &lastErr, &phone, &email); err != nil {
			t.Fatal(err)
		}
		total++
		byStatus[st]++
		ref := strconv.FormatInt(did, 10)
		malformed := (ch == "sms" && !validPhone.MatchString(phone)) || (ch == "email" && !validEmail.MatchString(email))
		switch st {
		case "delivered", "undelivered":
			if accepted[ref] != 1 {
				t.Errorf("delivery %s (%s) was accepted %d times by the provider", ref, ch, accepted[ref])
			}
			if malformed {
				t.Errorf("delivery %s to a malformed contact was accepted", ref)
			}
		case "failed":
			if !malformed || attempts != 1 {
				t.Errorf("delivery %s failed (attempts=%d, %q) but its contact is valid", ref, attempts, lastErr)
			}
			if accepted[ref] != 0 {
				t.Errorf("failed delivery %s was accepted by the provider", ref)
			}
		default:
			t.Errorf("delivery %s ended in status %q", ref, st)
		}
	}
	rows.Close()

	stats := env.provider.Stats()
	if total != wantSMS+wantEmail {
		t.Errorf("%d deliveries, want %d", total, wantSMS+wantEmail)
	}
	if stats.DuplicateAccept != 0 || stats.Accepted != byStatus["delivered"]+byStatus["undelivered"] {
		t.Errorf("provider accepted %d (duplicates %d); delivered+undelivered = %d",
			stats.Accepted, stats.DuplicateAccept, byStatus["delivered"]+byStatus["undelivered"])
	}
	// Each failure is counted once in the metrics.
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	failedMetric := 0
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		if strings.HasPrefix(line, "composer_deliveries_finished_total{") && strings.Contains(line, `status="failed"`) {
			f, _ := strconv.ParseFloat(line[strings.LastIndex(line, " ")+1:], 64)
			failedMetric += int(f)
		}
	}
	if failedMetric != byStatus["failed"] {
		t.Errorf("metrics count %d failed deliveries; the database has %d", failedMetric, byStatus["failed"])
	}
	// The chaos really happened, and was survived.
	if stats.ServerErrors == 0 || stats.RateLimited == 0 || stats.Dropped == 0 || stats.Replays == 0 {
		t.Errorf("chaos did not happen as configured: %+v", stats)
	}
	t.Logf("deliveries by status: %v; provider: %+v", byStatus, stats)
}

// recorder is a provider that records when each channel's sends happened.
type recorder struct {
	mu    sync.Mutex
	calls map[string][]time.Time
}

func (r *recorder) Send(ctx context.Context, m delivery.Message) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls[m.Channel] = append(r.calls[m.Channel], time.Now())
	return "msg-" + m.Reference, nil
}

func TestRateLimitPerChannel(t *testing.T) {
	rec := &recorder{calls: map[string][]time.Time{}}
	const perSecond = 25
	env := sendingServer(t, mockprovider.Config{}, rec, perSecond)
	id := readyCampaign(t, env.api.URL, "Trabzon") // about 150 users: 100+ messages per channel

	start := time.Now()
	if status, res := call(t, "POST", env.api.URL+"/api/campaigns/"+id+"/approve", ""); status != 200 {
		t.Fatalf("approve: %d %v", status, res)
	}
	waitUntil(t, "the campaign to complete", 30*time.Second, func() bool {
		_, c := call(t, "GET", env.api.URL+"/api/campaigns/"+id, "")
		return c["status"] == "completed"
	})

	rec.mu.Lock()
	defer rec.mu.Unlock()
	for _, ch := range delivery.Channels {
		times := rec.calls[ch]
		sort.Slice(times, func(i, j int) bool { return times[i].Before(times[j]) })
		if len(times) < 60 {
			t.Fatalf("%s: only %d sends", ch, len(times))
		}
		// In any one-second window, at most perSecond sends (+1 for the
		// limiter's burst of one).
		for i := range times {
			n := sort.Search(len(times), func(j int) bool { return times[j].After(times[i].Add(time.Second)) }) - i
			if n > perSecond+1 {
				t.Fatalf("%s: %d sends within one second starting at send %d (limit %d)", ch, n, i, perSecond)
			}
		}
		minDuration := time.Duration(len(times)-1) * time.Second / perSecond
		if took := times[len(times)-1].Sub(start); took < minDuration*9/10 {
			t.Fatalf("%s: %d sends took %s; at %d/s they need at least %s", ch, len(times), took, perSecond, minDuration)
		}
		t.Logf("%s: %d sends over %s", ch, len(times), times[len(times)-1].Sub(times[0]).Round(time.Millisecond))
	}
}

func postReceipt(t *testing.T, base string, rc map[string]any, secret string, at time.Time) (int, map[string]any) {
	t.Helper()
	body, _ := json.Marshal(rc)
	req, _ := http.NewRequest("POST", base+"/webhooks/provider", bytes.NewReader(body))
	req.Header.Set(webhook.TimestampHeader, strconv.FormatInt(at.Unix(), 10))
	req.Header.Set(webhook.SignatureHeader, webhook.Sign(secret, at, body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestReceiptWebhook(t *testing.T) {
	rec := &recorder{calls: map[string][]time.Time{}}
	env := sendingServer(t, mockprovider.Config{}, rec, 1000)
	id := readyCampaign(t, env.api.URL, "Trabzon")
	call(t, "POST", env.api.URL+"/api/campaigns/"+id+"/approve", "")
	waitUntil(t, "sending to finish", 30*time.Second, func() bool {
		_, c := call(t, "GET", env.api.URL+"/api/campaigns/"+id, "")
		return c["status"] == "completed"
	})
	var did int64
	env.pool.QueryRow(context.Background(), `SELECT id FROM deliveries WHERE campaign_id = $1 AND status = 'sent' LIMIT 1`, id).Scan(&did)
	ref := strconv.FormatInt(did, 10)
	receipt := func(status, msgID string) map[string]any {
		return map[string]any{"message_id": msgID, "reference": ref, "channel": "sms", "status": status, "timestamp": time.Now()}
	}
	now := time.Now()
	statusOf := func() string {
		var s string
		env.pool.QueryRow(context.Background(), `SELECT status FROM deliveries WHERE id = $1`, did).Scan(&s)
		return s
	}

	cases := []struct {
		name        string
		rc          map[string]any
		secret      string
		at          time.Time
		wantStatus  int
		wantApplied any
		after       string
	}{
		{"bad signature", receipt("delivered", "msg-"+ref), "wrong-secret", now, 401, nil, "sent"},
		{"stale timestamp", receipt("delivered", "msg-"+ref), webhookSecret, now.Add(-10 * time.Minute), 401, nil, "sent"},
		{"unknown status", receipt("opened", "msg-"+ref), webhookSecret, now, 400, nil, "sent"},
		{"someone else's message id", receipt("delivered", "msg-other"), webhookSecret, now, 200, false, "sent"},
		{"delivered", receipt("delivered", "msg-"+ref), webhookSecret, now, 200, true, "delivered"},
		{"duplicate", receipt("delivered", "msg-"+ref), webhookSecret, now, 200, false, "delivered"},
		{"no going backwards", receipt("undelivered", "msg-"+ref), webhookSecret, now, 200, false, "delivered"},
	}
	for _, c := range cases {
		status, body := postReceipt(t, env.api.URL, c.rc, c.secret, c.at)
		if status != c.wantStatus || (c.wantApplied != nil && body["applied"] != c.wantApplied) {
			t.Errorf("%s: %d %v, want %d applied=%v", c.name, status, body, c.wantStatus, c.wantApplied)
		}
		if got := statusOf(); got != c.after {
			t.Errorf("%s: delivery status %s, want %s", c.name, got, c.after)
		}
	}

	unknown := receipt("delivered", "msg-x")
	unknown["reference"] = "999999999"
	if status, body := postReceipt(t, env.api.URL, unknown, webhookSecret, now); status != 200 || body["applied"] != false {
		t.Errorf("unknown delivery: %d %v", status, body)
	}
}

func TestMetricsEndpoint(t *testing.T) {
	pool := testdb.New(t)
	m := metrics.New(pool)
	m.ProviderCall("sms", 429, 30*time.Millisecond)
	m.Webhook("applied")
	queue.Enqueue(context.Background(), pool, "send_sms", nil, queue.EnqueueOptions{})
	ts := httptest.NewServer(New(pool, quiet, Options{Metrics: m}).Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	for _, want := range []string{
		`composer_provider_requests_total{channel="sms",code="429"} 1`,
		`composer_webhooks_total{outcome="applied"} 1`,
		`composer_queue_jobs{kind="send_sms",status="queued"} 1`,
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("/metrics lacks %s", want)
		}
	}
}

func TestProgressStream(t *testing.T) {
	env := sendingServer(t, mockprovider.Config{
		LatencyMax: 2 * time.Millisecond, WebhookDelayMin: 20 * time.Millisecond, WebhookDelayMax: 60 * time.Millisecond, Seed: 3,
	}, nil, 60) // slow enough that sending spans a few seconds
	id := readyCampaign(t, env.api.URL, "Trabzon")
	stream := openStream(t, env.api.URL+"/api/campaigns/"+id+"/events")
	time.Sleep(200 * time.Millisecond)
	if status, res := call(t, "POST", env.api.URL+"/api/campaigns/"+id+"/approve", ""); status != 200 {
		t.Fatalf("approve: %d %v", status, res)
	}

	var progress []sseEvent
	for _, ev := range waitEventWithin(t, stream, "end", 60*time.Second) {
		if ev.name == "progress" {
			progress = append(progress, ev)
		}
	}
	if len(progress) < 3 {
		t.Fatalf("only %d progress events", len(progress))
	}
	// Throttled: at most one every 500 ms (the final one may follow sooner).
	for i := 1; i < len(progress)-1; i++ {
		if gap := progress[i].at.Sub(progress[i-1].at); gap < 400*time.Millisecond {
			t.Errorf("progress events %d and %d only %s apart", i-1, i, gap)
		}
	}

	final := progress[len(progress)-1].data
	if final["status"] != "completed" || final["pending_receipts"].(float64) != 0 {
		t.Fatalf("final progress: status=%v pending=%v", final["status"], final["pending_receipts"])
	}
	counts := final["counts"].(map[string]any)
	grid := final["grid"].(map[string]any)
	var dbTotal int
	env.pool.QueryRow(context.Background(), `SELECT count(*) FROM deliveries WHERE campaign_id = $1`, id).Scan(&dbTotal)
	gridTotal := 0
	for _, ch := range delivery.Channels {
		g := grid[ch].(string)
		gridTotal += len(g)
		perStatus := map[string]int{}
		for _, c := range g {
			if !strings.ContainsRune("du", c) && c != 'f' {
				t.Fatalf("%s grid has %q after settling", ch, c)
			}
			perStatus[map[rune]string{'d': "delivered", 'u': "undelivered", 'f': "failed"}[c]]++
		}
		for st, n := range perStatus {
			if int(counts[ch].(map[string]any)[st].(float64)) != n {
				t.Errorf("%s %s: counts say %v, grid has %d", ch, st, counts[ch].(map[string]any)[st], n)
			}
		}
	}
	if gridTotal != dbTotal {
		t.Fatalf("grid has %d squares, database %d deliveries", gridTotal, dbTotal)
	}
	if peak := maxThroughput(progress); peak > 61*2 {
		t.Errorf("reported throughput %.0f/s exceeds the two channels' limits", peak)
	}
}

func waitEventWithin(t *testing.T, ch <-chan sseEvent, want string, d time.Duration) []sseEvent {
	t.Helper()
	var seen []sseEvent
	timeout := time.After(d)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatalf("stream closed before %q", want)
			}
			seen = append(seen, ev)
			if ev.name == want {
				return seen
			}
		case <-timeout:
			t.Fatalf("no %q within %s; saw %d events", want, d, len(seen))
		}
	}
}

func maxThroughput(progress []sseEvent) float64 {
	peak := 0.0
	for _, ev := range progress {
		sum := 0.0
		for _, v := range ev.data["throughput"].(map[string]any) {
			sum += v.(float64)
		}
		peak = max(peak, sum)
	}
	return peak
}

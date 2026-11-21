package api

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"campaign-composer/internal/backoff"
	"campaign-composer/internal/campaign"
	"campaign-composer/internal/composer"
	"campaign-composer/internal/events"
	"campaign-composer/internal/llm"
	"campaign-composer/internal/llm/llmtest"
	"campaign-composer/internal/queue"
	"campaign-composer/internal/seed"
	"campaign-composer/internal/testdb"
)

const contentAnswer = `{"language":"en","sms":"Hi {{first_name}}, your cart misses you! 30% off today.",` +
	`"email_subject":"{{first_name}}, 30% off your cart","email_body":"Hi {{first_name}},\n\nYour cart is waiting.\n\nBest,\nThe team"}`

// contentServer runs the API with a hub and a content worker, as main does.
func contentServer(t *testing.T, fake *llmtest.Fake) (*httptest.Server, *pgxpool.Pool) {
	t.Helper()
	pool := testdb.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	if _, err := seed.Users(ctx, pool, seed.Options{N: 5000, Seed: 42, Now: fixedNow}); err != nil {
		t.Fatal(err)
	}
	hub := events.NewHub(pool, quiet)
	go hub.Run(ctx)
	<-hub.Ready()

	comp := composer.New(fake, quiet)
	comp.Retry = llm.RetryPolicy{MaxAttempts: 2, Backoff: backoff.Policy{Base: time.Millisecond}, MaxWait: time.Second}
	clock := func() time.Time { return fixedNow }
	svc := campaign.NewService(pool, comp, clock)
	worker := &queue.Worker{Pool: pool, Hub: hub, Log: quiet, Kind: campaign.JobGenerateContent,
		Handler: svc.HandleContentJob, OnFailed: svc.OnContentJobFailed, Concurrency: 2,
		Backoff: backoff.Policy{Base: time.Millisecond}}
	done := make(chan struct{})
	go func() { worker.Run(ctx); close(done) }()

	s := New(pool, quiet, Options{Hub: hub, Now: clock, Campaigns: svc})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(func() { s.CloseStreams(); ts.Close(); cancel(); <-done })
	return ts, pool
}

type sseEvent struct {
	name string
	data map[string]any
	at   time.Time
}

// openStream starts reading a campaign's events into a channel.
func openStream(t *testing.T, url string) <-chan sseEvent {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	ch := make(chan sseEvent, 256)
	go func() {
		defer close(ch)
		sc := bufio.NewScanner(resp.Body)
		var name string
		for sc.Scan() {
			line := sc.Text()
			if v, ok := strings.CutPrefix(line, "event: "); ok {
				name = v
			}
			if v, ok := strings.CutPrefix(line, "data: "); ok {
				var data map[string]any
				json.Unmarshal([]byte(v), &data)
				ch <- sseEvent{name, data, time.Now()}
			}
		}
	}()
	return ch
}

// waitEvent collects events until one named want arrives.
func waitEvent(t *testing.T, ch <-chan sseEvent, want string) []sseEvent {
	t.Helper()
	var seen []sseEvent
	timeout := time.After(10 * time.Second)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatalf("stream closed before %q; saw %v", want, names(seen))
			}
			seen = append(seen, ev)
			if ev.name == want {
				return seen
			}
		case <-timeout:
			t.Fatalf("no %q event; saw %v", want, names(seen))
		}
	}
}

func names(evs []sseEvent) []string {
	out := make([]string, len(evs))
	for i, e := range evs {
		out[i] = e.name
	}
	return out
}

func readyAudience(t *testing.T, ts *httptest.Server) string {
	t.Helper()
	id := createCampaign(t, ts)
	if status, res := call(t, "POST", ts.URL+"/api/campaigns/"+id+"/audience", ""); status != 200 {
		t.Fatalf("audience: %d %v", status, res)
	}
	return id
}

func TestContentStreamsAndSaves(t *testing.T) {
	ts, _ := contentServer(t, llmtest.New(llmtest.Text(claudeAnswer), llmtest.Text(contentAnswer)))
	id := readyAudience(t, ts)
	stream := openStream(t, ts.URL+"/api/campaigns/"+id+"/events")

	status, c := call(t, "POST", ts.URL+"/api/campaigns/"+id+"/content", `{}`)
	if status != http.StatusAccepted || c["content_state"] != "generating" {
		t.Fatalf("start: %d %v", status, c)
	}
	seen := waitEvent(t, stream, "content_ready")

	var sms strings.Builder
	for _, ev := range seen {
		if ev.name == "delta" && ev.data["field"] == "sms" {
			sms.WriteString(ev.data["text"].(string))
		}
	}
	_, c = call(t, "GET", ts.URL+"/api/campaigns/"+id, "")
	if c["status"] != "content_ready" || c["content_state"] != "ready" || c["language"] != "en" {
		t.Fatalf("campaign after generation: %v", c)
	}
	if sms.String() != c["sms"] {
		t.Fatalf("streamed SMS %q differs from saved SMS %q", sms.String(), c["sms"])
	}
	if check := c["content_check"].(map[string]any); check["sms_segments"].(float64) != 1 {
		t.Fatalf("check = %v", check)
	}
}

func TestContentFailureIsRecorded(t *testing.T) {
	bad := strings.Replace(contentAnswer, "{{first_name}}, your", "{{nickname}}, your", 1)
	ts, _ := contentServer(t, llmtest.New(llmtest.Text(claudeAnswer), llmtest.Text(bad), llmtest.Text(bad)))
	id := readyAudience(t, ts)
	stream := openStream(t, ts.URL+"/api/campaigns/"+id+"/events")
	call(t, "POST", ts.URL+"/api/campaigns/"+id+"/content", "")

	seen := waitEvent(t, stream, "content_failed")
	if code := seen[len(seen)-1].data["data"].(map[string]any)["code"]; code != "llm_invalid_output" {
		t.Fatalf("failure code = %v", code)
	}
	_, c := call(t, "GET", ts.URL+"/api/campaigns/"+id, "")
	if c["status"] != "audience_ready" || c["content_state"] != "failed" {
		t.Fatalf("campaign = %v", c)
	}
}

func TestHandEditedContent(t *testing.T) {
	ts, _ := contentServer(t, llmtest.New(llmtest.Text(claudeAnswer)))
	id := readyAudience(t, ts)

	status, res := call(t, "PUT", ts.URL+"/api/campaigns/"+id+"/content",
		`{"language":"tr","sms":"Merhaba {{nickname}}","email_subject":"Selam","email_body":"Merhaba"}`)
	if status != 422 || res["error"].(map[string]any)["code"] != "invalid_content" {
		t.Fatalf("invalid edit: %d %v", status, res)
	}
	status, c := call(t, "PUT", ts.URL+"/api/campaigns/"+id+"/content",
		`{"language":"tr","sms":"Merhaba {{first_name}}, sepetindekiler seni bekliyor, şimdi tamamla!","email_subject":"Sepetin seni bekliyor","email_body":"Merhaba {{first_name}},\n\nSepetin seni bekliyor."}`)
	if status != 200 || c["status"] != "content_ready" || c["content_check"].(map[string]any)["sms_encoding"] != "UCS-2" {
		t.Fatalf("valid edit: %d %v", status, c)
	}
}

func TestAudienceChangeRechecksContent(t *testing.T) {
	ts, _ := contentServer(t, llmtest.New())
	id := createCampaign(t, ts)
	rule := func(city string) string {
		return `{"rule":{"match":"all","conditions":[{"field":"city","op":"eq","value":"` + city + `"}]}}`
	}
	call(t, "PUT", ts.URL+"/api/campaigns/"+id+"/audience", rule("Ankara"))

	// 154 letters + "Ankara" (6) = exactly 160.
	sms := strings.Repeat("a", 154) + "{{city}}"
	status, c := call(t, "PUT", ts.URL+"/api/campaigns/"+id+"/content",
		`{"language":"en","sms":"`+sms+`","email_subject":"Hi","email_body":"Hello"}`)
	if status != 200 || c["status"] != "content_ready" {
		t.Fatalf("content for Ankara: %d %v", status, c)
	}

	// Eskişehir has 9 letters: the same SMS is now 163 characters.
	_, res := call(t, "PUT", ts.URL+"/api/campaigns/"+id+"/audience", rule("Eskişehir"))
	c = res["campaign"].(map[string]any)
	if c["status"] != "audience_ready" || c["content_state"] != "invalid" {
		t.Fatalf("after a longer city: status=%v content_state=%v", c["status"], c["content_state"])
	}
	if p := c["content_error"].(map[string]any)["problems"].([]any)[0].(string); !strings.Contains(p, "shorten it by 3") {
		t.Fatalf("problem = %q", p)
	}

	// Back to Ankara: valid again, without editing the messages.
	_, res = call(t, "PUT", ts.URL+"/api/campaigns/"+id+"/audience", rule("Ankara"))
	if c = res["campaign"].(map[string]any); c["status"] != "content_ready" || c["content_state"] != "ready" || c["content_error"] != nil {
		t.Fatalf("back to Ankara: %v", c)
	}
}

func TestNewestGenerationWins(t *testing.T) {
	release := make(chan struct{})
	older := strings.Replace(contentAnswer, "misses you", "OLDER", 1)
	fake := llmtest.New(llmtest.Text(claudeAnswer), llmtest.Wait(release, llmtest.Text(older)), llmtest.Text(contentAnswer))
	ts, _ := contentServer(t, fake)
	id := readyAudience(t, ts)
	stream := openStream(t, ts.URL+"/api/campaigns/"+id+"/events")

	call(t, "POST", ts.URL+"/api/campaigns/"+id+"/content", "")
	for len(fake.Requests()) < 2 { // the first generation is now waiting
		time.Sleep(10 * time.Millisecond)
	}
	// While it waits, the audience is locked...
	if status, res := call(t, "POST", ts.URL+"/api/campaigns/"+id+"/audience", ""); status != 409 || res["error"].(map[string]any)["code"] != "generating" {
		t.Fatalf("audience during generation: %d %v", status, res)
	}
	// ...and a second request supersedes it.
	call(t, "POST", ts.URL+"/api/campaigns/"+id+"/content", "")
	waitEvent(t, stream, "content_ready")
	close(release)
	time.Sleep(300 * time.Millisecond) // let the older job finish and (not) save

	_, c := call(t, "GET", ts.URL+"/api/campaigns/"+id, "")
	if strings.Contains(c["sms"].(string), "OLDER") {
		t.Fatalf("an older generation overwrote the newer one: %q", c["sms"])
	}
}

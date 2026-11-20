package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"campaign-composer/internal/backoff"
	"campaign-composer/internal/campaign"
	"campaign-composer/internal/composer"
	"campaign-composer/internal/llm"
	"campaign-composer/internal/llm/llmtest"
	"campaign-composer/internal/seed"
	"campaign-composer/internal/testdb"
)

const brief = "Remind Istanbul users who added items to their cart but didn't buy in the last 30 days about a 30% discount."

const claudeAnswer = `{"rule":{"match":"all","conditions":[
  {"field":"city","op":"eq","value":"Istanbul"},
  {"field":"last_cart_at","op":"within_days","value":30},
  {"field":"last_purchase_at","op":"not_within_days","value":30}]},
 "summary":"İstanbul users with an abandoned cart.","assumptions":[]}`

func campaignServer(t *testing.T, fake *llmtest.Fake) *httptest.Server {
	t.Helper()
	pool := testdb.New(t)
	if _, err := seed.Users(context.Background(), pool, seed.Options{N: 5000, Seed: 42, Now: fixedNow}); err != nil {
		t.Fatal(err)
	}
	comp := composer.New(fake, quiet)
	comp.Retry = llm.RetryPolicy{MaxAttempts: 2, Backoff: backoff.Policy{Base: time.Millisecond}, MaxWait: time.Second}
	clock := func() time.Time { return fixedNow }
	s := New(pool, quiet, Options{Now: clock, Campaigns: campaign.NewService(pool, comp, clock)})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func call(t *testing.T, method, url, body string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("%s %s: not JSON: %s", method, url, raw)
	}
	return resp.StatusCode, out
}

func createCampaign(t *testing.T, ts *httptest.Server) string {
	t.Helper()
	status, c := call(t, "POST", ts.URL+"/api/campaigns", `{"brief": `+string(mustJSON(brief))+`}`)
	if status != http.StatusCreated || c["status"] != "draft" {
		t.Fatalf("create: %d %v", status, c)
	}
	return c["id"].(string)
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

func TestAudienceFlow(t *testing.T) {
	fake := llmtest.New(llmtest.Text(claudeAnswer),
		llmtest.Text(strings.Replace(claudeAnswer, `"Istanbul"`, `"Ankara"`, 1)))
	ts := campaignServer(t, fake)
	id := createCampaign(t, ts)

	status, res := call(t, "POST", ts.URL+"/api/campaigns/"+id+"/audience", "")
	if status != 200 {
		t.Fatalf("generate: %d %v", status, res)
	}
	c := res["campaign"].(map[string]any)
	if c["status"] != "audience_ready" || c["audience_total"].(float64) != 322 || c["audience_summary"] == "" {
		t.Fatalf("campaign after audience: %v", c)
	}
	if p := res["preview"].(map[string]any); len(p["sample"].([]any)) != 5 {
		t.Fatalf("preview = %v", p)
	}

	// Refine goes back to the LLM with the current rule.
	status, res = call(t, "POST", ts.URL+"/api/campaigns/"+id+"/audience/refine", `{"instruction":"Ankara instead"}`)
	if status != 200 {
		t.Fatalf("refine: %d %v", status, res)
	}
	if got := res["campaign"].(map[string]any)["rule"].(map[string]any)["conditions"].([]any)[0].(map[string]any)["value"]; got != "Ankara" {
		t.Fatalf("refined city = %v", got)
	}

	// A hand edit is validated and replaces the summary.
	status, res = call(t, "PUT", ts.URL+"/api/campaigns/"+id+"/audience",
		`{"rule":{"match":"all","conditions":[{"field":"city","op":"eq","value":"izmir"}]}}`)
	c = res["campaign"].(map[string]any)
	if status != 200 || c["audience_summary"] != "" || c["rule"].(map[string]any)["conditions"].([]any)[0].(map[string]any)["value"] != "İzmir" {
		t.Fatalf("hand edit: %d %v", status, c)
	}
	status, res = call(t, "PUT", ts.URL+"/api/campaigns/"+id+"/audience",
		`{"rule":{"match":"all","conditions":[{"field":"city","op":"eq","value":"Paris"}]}}`)
	if status != 422 || res["error"].(map[string]any)["code"] != "invalid_rule" {
		t.Fatalf("bad hand edit: %d %v", status, res)
	}

	// The list shows the campaign.
	_, list := call(t, "GET", ts.URL+"/api/campaigns", "")
	if n := len(list["campaigns"].([]any)); n != 1 {
		t.Fatalf("list has %d campaigns", n)
	}
}

func TestAudienceErrorsAreActionable(t *testing.T) {
	cases := []struct {
		name   string
		steps  []llmtest.Step
		status int
		code   string
	}{
		{"invalid twice", []llmtest.Step{llmtest.Text(`{"rule":{"match":"all","conditions":[{"field":"age","op":"gt","value":30}]},"summary":"","assumptions":[]}`), llmtest.Text(`nope`)},
			422, "llm_invalid_output"},
		{"refused", []llmtest.Step{llmtest.Fail(llm.ErrRefused)}, 422, "llm_refused"},
		{"overloaded", []llmtest.Step{llmtest.Fail(&llm.StatusError{Provider: "claude", StatusCode: 529}),
			llmtest.Fail(&llm.StatusError{Provider: "claude", StatusCode: 529})}, 502, "llm_unavailable"},
		{"misconfigured", []llmtest.Step{llmtest.Fail(&llm.StatusError{Provider: "claude", StatusCode: 400,
			Message: "This API key is not scoped to a workspace"})}, 502, "llm_rejected"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ts := campaignServer(t, llmtest.New(c.steps...))
			id := createCampaign(t, ts)
			status, res := call(t, "POST", ts.URL+"/api/campaigns/"+id+"/audience", "")
			if status != c.status || res["error"].(map[string]any)["code"] != c.code {
				t.Fatalf("got %d %v, want %d %s", status, res, c.status, c.code)
			}
			// The campaign is still a draft and can be retried.
			_, got := call(t, "GET", ts.URL+"/api/campaigns/"+id, "")
			if got["status"] != "draft" {
				t.Fatalf("status after failure = %v", got["status"])
			}
		})
	}
}

func TestCampaignValidation(t *testing.T) {
	ts := campaignServer(t, llmtest.New())
	if status, _ := call(t, "POST", ts.URL+"/api/campaigns", `{"brief":"   "}`); status != 400 {
		t.Errorf("empty brief: %d", status)
	}
	if status, _ := call(t, "POST", ts.URL+"/api/campaigns", `{"brief":"`+strings.Repeat("a", 2001)+`"}`); status != 400 {
		t.Errorf("long brief: %d", status)
	}
	for _, id := range []string{"00000000-0000-0000-0000-000000000000", "not-a-uuid"} {
		if status, _ := call(t, "GET", ts.URL+"/api/campaigns/"+id, ""); status != 404 {
			t.Errorf("GET %s: %d, want 404", id, status)
		}
	}
}

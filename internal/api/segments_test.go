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

	"campaign-composer/internal/seed"
	"campaign-composer/internal/testdb"
)

var fixedNow = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

func seededServer(t *testing.T) *httptest.Server {
	t.Helper()
	pool := testdb.New(t)
	if _, err := seed.Users(context.Background(), pool, seed.Options{N: 5000, Seed: 42, Now: fixedNow}); err != nil {
		t.Fatal(err)
	}
	s := New(pool, quiet, Options{Now: func() time.Time { return fixedNow }})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func postJSON(t *testing.T, url, body string) (int, map[string]any) {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("response is not JSON: %s", raw)
	}
	return resp.StatusCode, out
}

func TestSegmentFields(t *testing.T) {
	ts := seededServer(t)
	resp, err := http.Get(ts.URL + "/api/segments/fields")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body struct {
		Fields []struct {
			Name   string   `json:"name"`
			Label  string   `json:"label"`
			Ops    []string `json:"ops"`
			Values []string `json:"values"`
		} `json:"fields"`
	}
	json.NewDecoder(resp.Body).Decode(&body)
	if len(body.Fields) == 0 || body.Fields[0].Name != "city" || len(body.Fields[0].Values) == 0 || body.Fields[0].Label == "" {
		t.Fatalf("fields = %+v", body.Fields)
	}
}

func TestSegmentPreview(t *testing.T) {
	ts := seededServer(t)
	status, body := postJSON(t, ts.URL+"/api/segments/preview", `{"rule": {"match":"all","conditions":[
		{"field":"city","op":"eq","value":"istanbul"},
		{"field":"last_cart_at","op":"within_days","value":30},
		{"field":"last_purchase_at","op":"not_within_days","value":30}]}}`)
	if status != 200 {
		t.Fatalf("status %d: %v", status, body)
	}
	preview := body["preview"].(map[string]any)
	if total := preview["total"].(float64); total != 322 {
		t.Errorf("total = %v, want 322 for the seeded data and fixed clock", total)
	}
	if len(preview["sample"].([]any)) != 5 {
		t.Errorf("sample = %v", preview["sample"])
	}
	cond := body["rule"].(map[string]any)["conditions"].([]any)[0].(map[string]any)
	if cond["value"] != "İstanbul" {
		t.Errorf("rule not normalized: %v", cond)
	}
}

func TestSegmentPreviewRejectsBadRules(t *testing.T) {
	ts := seededServer(t)

	status, body := postJSON(t, ts.URL+"/api/segments/preview", `{"rule": {"match":"all","conditions":[
		{"field":"city","op":"eq","value":"Paris"},
		{"field":"total_spent","op":"gt","value":-1}]}}`)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("status %d, want 422", status)
	}
	e := body["error"].(map[string]any)
	if e["code"] != "invalid_rule" || len(e["problems"].([]any)) != 2 {
		t.Fatalf("error = %v", e)
	}

	status, _ = postJSON(t, ts.URL+"/api/segments/preview", `{"rule": {"match":"all","conditions":[]}, "sql": "x"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("unknown request field: status %d, want 400", status)
	}
}

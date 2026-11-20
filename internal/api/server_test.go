package api

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"campaign-composer/internal/events"
	"campaign-composer/internal/testdb"
)

func TestMain(m *testing.M) { os.Exit(testdb.Main(m)) }

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func newTestServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	pool := testdb.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	hub := events.NewHub(pool, quiet)
	go hub.Run(ctx)
	<-hub.Ready()
	s := New(pool, quiet, Options{Hub: hub})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(func() { s.CloseStreams(); ts.Close(); cancel() })
	return s, ts
}

func TestHealthAndReady(t *testing.T) {
	_, ts := newTestServer(t)
	for path, want := range map[string]string{"/healthz": `"ok"`, "/readyz": `"ready"`} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || !strings.Contains(string(body), want) {
			t.Errorf("%s: %d %s", path, resp.StatusCode, body)
		}
	}
	resp, _ := http.Get(ts.URL + "/api/nope")
	if resp.StatusCode != 404 || resp.Header.Get("Content-Type") != "application/json" {
		t.Errorf("unknown api path: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	resp.Body.Close()
}

func TestDecodeJSONIsStrict(t *testing.T) {
	type req struct {
		Brief string `json:"brief"`
	}
	for body, wantOK := range map[string]bool{
		`{"brief": "hi"}`:                true,
		`{"brief": "hi", "extra": 1}`:    false, // unknown field
		`{"brief": "hi"} {"brief": "x"}`: false, // trailing data
		`not json`:                       false,
	} {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		var v req
		if ok := decodeJSON(rec, r, &v); ok != wantOK {
			t.Errorf("%q: ok=%v, want %v (%s)", body, ok, wantOK, rec.Body.String())
		}
	}
}

func TestRecoverPanics(t *testing.T) {
	s := New(nil, quiet, Options{})
	h := s.recoverPanics(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != 500 || strings.Contains(rec.Body.String(), "boom") {
		t.Fatalf("got %d %q; want a 500 that hides the panic", rec.Code, rec.Body.String())
	}
}

// readEvents collects event names from an SSE body, with delta payloads inlined.
func readEvents(t *testing.T, body io.Reader, n int) []string {
	t.Helper()
	sc := bufio.NewScanner(body)
	var got []string
	for len(got) < n && sc.Scan() {
		line := sc.Text()
		if name, ok := strings.CutPrefix(line, "event: "); ok {
			got = append(got, name)
		}
		if data, ok := strings.CutPrefix(line, "data: "); ok && len(got) > 0 && got[len(got)-1] == "delta" {
			got[len(got)-1] = "delta:" + data
		}
	}
	return got
}

func TestStreamReplaysThenFollowsLive(t *testing.T) {
	s, _ := newTestServer(t)
	ctx := context.Background()
	const topic = "campaign:1"
	var finished atomic.Bool

	mux := http.NewServeMux()
	mux.HandleFunc("GET /stream", func(w http.ResponseWriter, r *http.Request) {
		s.streamTopic(w, r, topic, func(context.Context) (bool, error) { return finished.Load(), nil })
	})
	streamSrv := httptest.NewServer(mux)
	defer streamSrv.Close()

	events.Add(ctx, s.pool, topic, "created", nil)
	events.Add(ctx, s.pool, topic, "audience_ready", nil)

	// Reconnect after event 1: only event 2 is replayed.
	req, _ := http.NewRequest(http.MethodGet, streamSrv.URL+"/stream", nil)
	req.Header.Set("Last-Event-ID", "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}

	go func() {
		time.Sleep(200 * time.Millisecond)
		events.Delta(ctx, s.pool, topic, "Merhaba")
		time.Sleep(100 * time.Millisecond)
		finished.Store(true)
		events.Add(ctx, s.pool, topic, "content_ready", nil)
	}()

	got := readEvents(t, resp.Body, 4)
	want := []string{"audience_ready", `delta:{"text":"Merhaba"}`, "content_ready", "end"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("events = %v, want %v", got, want)
	}
}

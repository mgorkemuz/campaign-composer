package llm_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"campaign-composer/internal/llm"
)

const testKey = "test-key-123"

// fakeClaude plays the Messages API: it records the request and answers with
// the given handler.
type fakeClaude struct {
	body   map[string]any
	header http.Header
}

func (f *fakeClaude) server(t *testing.T, answer func(w http.ResponseWriter)) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &f.body)
		f.header = r.Header.Clone()
		answer(w)
	}))
	t.Cleanup(ts.Close)
	return ts
}

// sse writes a streamed answer whose text arrives in the given chunks.
func sse(stopReason string, chunks ...string) func(w http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/event-stream")
		send := func(event string, data any) {
			b, _ := json.Marshal(data)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
		}
		send("message_start", map[string]any{"type": "message_start", "message": map[string]any{
			"id": "msg_1", "type": "message", "role": "assistant", "model": "claude-haiku-5-5",
			"content": []any{}, "stop_reason": nil, "stop_sequence": nil,
			"usage": map[string]any{"input_tokens": 42, "output_tokens": 1}}})
		send("content_block_start", map[string]any{"type": "content_block_start", "index": 0,
			"content_block": map[string]any{"type": "text", "text": ""}})
		for _, c := range chunks {
			send("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0,
				"delta": map[string]any{"type": "text_delta", "text": c}})
		}
		send("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
		send("message_delta", map[string]any{"type": "message_delta",
			"delta": map[string]any{"stop_reason": stopReason, "stop_sequence": nil},
			"usage": map[string]any{"output_tokens": 17}})
		send("message_stop", map[string]any{"type": "message_stop"})
	}
}

func httpError(status int, retryAfter, message string) func(w http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		if retryAfter != "" {
			w.Header().Set("Retry-After", retryAfter)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		fmt.Fprintf(w, `{"type":"error","error":{"type":"some_error","message":%q}}`, message)
	}
}

func newClaude(ts *httptest.Server, model string) *llm.Anthropic {
	return llm.NewAnthropic(llm.AnthropicOptions{APIKey: testKey, Model: model, BaseURL: ts.URL})
}

var schema = map[string]any{"type": "object", "properties": map[string]any{"ok": map[string]any{"type": "boolean"}},
	"required": []string{"ok"}, "additionalProperties": false}

func TestClaudeStreamsStructuredOutput(t *testing.T) {
	var f fakeClaude
	ts := f.server(t, sse("end_turn", `{"ok"`, `: true}`))

	var streamed []string
	resp, err := newClaude(ts, "claude-haiku-5-5").Generate(context.Background(), llm.Request{
		Task: "audience", System: "You build audiences.", Schema: schema,
		Messages: []llm.Message{{Role: llm.RoleUser, Text: "brief"}, {Role: llm.RoleAssistant, Text: "{}"}, {Role: llm.RoleUser, Text: "fix it"}},
	}, func(s string) { streamed = append(streamed, s) })
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != `{"ok": true}` || strings.Join(streamed, "|") != `{"ok"|: true}` {
		t.Fatalf("text=%q streamed=%q", resp.Text, streamed)
	}
	if resp.Usage.InputTokens != 42 || resp.Usage.OutputTokens != 17 || resp.Model != "claude-haiku-5-5" {
		t.Errorf("resp = %+v", resp)
	}

	// The request the SDK sent.
	// This SDK version sends the API key as a bearer token.
	if f.header.Get("Authorization") != "Bearer "+testKey {
		t.Errorf("the configured API key was not sent")
	}
	b := f.body
	if b["model"] != "claude-haiku-5-5" || b["stream"] != true {
		t.Errorf("model/stream: %v / %v", b["model"], b["stream"])
	}
	oc, _ := b["output_config"].(map[string]any)
	format, _ := oc["format"].(map[string]any)
	if oc["effort"] != "low" || format["type"] != "json_schema" || format["schema"] == nil {
		t.Errorf("output_config = %v", oc)
	}
	if msgs, _ := b["messages"].([]any); len(msgs) != 3 || msgs[1].(map[string]any)["role"] != "assistant" {
		t.Errorf("messages = %v", b["messages"])
	}
	if _, ok := b["fallbacks"]; ok {
		t.Error("Haiku has no server-side fallback; the field must not be sent")
	}
	if _, ok := b["temperature"]; ok {
		t.Error("sampling parameters must not be sent")
	}
}

func TestClaudeAddsFallbackForSupportingModels(t *testing.T) {
	var f fakeClaude
	ts := f.server(t, sse("end_turn", `{"ok": true}`))
	if _, err := newClaude(ts, "claude-opus-5-5").Generate(context.Background(), llm.Request{Messages: []llm.Message{{Role: llm.RoleUser, Text: "hi"}}}, nil); err != nil {
		t.Fatal(err)
	}
	if f.header.Get("Anthropic-Workspace-Id") != "" {
		t.Error("no workspace configured, but the header was sent")
	}
	if f.body["fallbacks"] != "default" || !strings.Contains(f.header.Get("Anthropic-Beta"), "server-side-fallback-2026-07-01") {
		t.Fatalf("fallbacks=%v beta=%q", f.body["fallbacks"], f.header.Get("Anthropic-Beta"))
	}
}

func TestClaudeErrors(t *testing.T) {
	cases := []struct {
		name      string
		answer    func(w http.ResponseWriter)
		status    int
		temporary bool
		after     time.Duration
		is        error
	}{
		{"rate limit", httpError(429, "7", "slow down"), 429, true, 7 * time.Second, nil},
		{"overloaded", httpError(529, "", "overloaded"), 529, true, 0, nil},
		{"bad request", httpError(400, "", "bad schema"), 400, false, 0, nil},
		{"refusal", sse("refusal", "I can't"), 0, false, 0, llm.ErrRefused},
		{"truncated", sse("max_tokens", `{"ok":`), 0, false, 0, llm.ErrTruncated},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var f fakeClaude
			ts := f.server(t, c.answer)
			_, err := newClaude(ts, "claude-haiku-5-5").Generate(context.Background(), llm.Request{Messages: []llm.Message{{Role: llm.RoleUser, Text: "hi"}}}, nil)
			if c.is != nil {
				if !errors.Is(err, c.is) {
					t.Fatalf("err = %v, want %v", err, c.is)
				}
				return
			}
			var se *llm.StatusError
			if !errors.As(err, &se) {
				t.Fatalf("err = %v, want a StatusError", err)
			}
			if se.StatusCode != c.status || se.Temporary() != c.temporary || se.RetryAfter != c.after {
				t.Fatalf("status=%d temporary=%v after=%s", se.StatusCode, se.Temporary(), se.RetryAfter)
			}
		})
	}
}

func TestClaudeScrubsKeyFromErrors(t *testing.T) {
	var f fakeClaude
	ts := f.server(t, httpError(401, "", "invalid x-api-key "+testKey))
	_, err := newClaude(ts, "claude-haiku-5-5").Generate(context.Background(), llm.Request{Messages: []llm.Message{{Role: llm.RoleUser, Text: "hi"}}}, nil)
	if err == nil || strings.Contains(err.Error(), testKey) {
		t.Fatalf("err = %v; the key must never appear in errors", err)
	}
}

func TestClaudeSendsWorkspaceID(t *testing.T) {
	var f fakeClaude
	ts := f.server(t, sse("end_turn", `{"ok": true}`))
	c := llm.NewAnthropic(llm.AnthropicOptions{APIKey: testKey, Model: "claude-haiku-5-5", BaseURL: ts.URL, WorkspaceID: "wrkspc_test"})
	if _, err := c.Generate(context.Background(), llm.Request{Messages: []llm.Message{{Role: llm.RoleUser, Text: "hi"}}}, nil); err != nil {
		t.Fatal(err)
	}
	if got := f.header.Get("Anthropic-Workspace-Id"); got != "wrkspc_test" {
		t.Fatalf("anthropic-workspace-id = %q", got)
	}
}

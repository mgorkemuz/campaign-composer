package composer

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"campaign-composer/internal/backoff"
	"campaign-composer/internal/llm"
	"campaign-composer/internal/llm/llmtest"
	"campaign-composer/internal/schema"
	"campaign-composer/internal/segment"
)

const exampleBrief = "Remind Istanbul users who added items to their cart but didn't buy in the last 30 days about a 30% discount, in a friendly, short tone."

const goodAnswer = `{"rule":{"match":"all","conditions":[
  {"field":"city","op":"eq","value":"Istanbul"},
  {"field":"last_cart_at","op":"within_days","value":30},
  {"field":"last_purchase_at","op":"not_within_days","value":30}]},
 "summary":"İstanbul users with an abandoned cart in the last 30 days.",
 "assumptions":["\"Didn't buy\" means no purchase in the same 30 days."]}`

func newComposer(p llm.Provider) *Composer {
	c := New(p, slog.New(slog.NewTextHandler(io.Discard, nil)))
	c.Retry = llm.RetryPolicy{MaxAttempts: 3, Backoff: backoff.Policy{Base: time.Millisecond}, MaxWait: time.Second}
	return c
}

func TestGenerateAudience(t *testing.T) {
	fake := llmtest.New(llmtest.Text(goodAnswer))
	aud, err := newComposer(fake).GenerateAudience(context.Background(), exampleBrief)
	if err != nil {
		t.Fatal(err)
	}
	if aud.Attempts != 1 || len(aud.Rule.Conditions) != 3 || string(aud.Rule.Conditions[0].Value) != `"İstanbul"` {
		t.Fatalf("audience = %+v", aud)
	}
	if aud.Summary == "" || len(aud.Assumptions) != 1 {
		t.Errorf("summary/assumptions lost: %+v", aud)
	}

	req := fake.Requests()[0]
	if req.Task != TaskAudience || req.Schema == nil {
		t.Errorf("task=%q schema=%v", req.Task, req.Schema != nil)
	}
	for _, want := range []string{"last_cart_at", "İstanbul", "not_within_days"} {
		if !strings.Contains(req.System, want) {
			t.Errorf("system prompt does not list %q", want)
		}
	}
	if user := req.Messages[0].Text; tagged(user, "brief") != exampleBrief {
		t.Errorf("brief not passed in tags: %q", user)
	}
}

func TestAudienceSchemaAcceptsAnswerAndRejectsJunk(t *testing.T) {
	raw, _ := json.Marshal(audienceSchema())
	s, err := schema.Compile(raw)
	if err != nil {
		t.Fatalf("audience schema does not compile: %v", err)
	}
	if _, err := s.Parse(goodAnswer); err != nil {
		t.Fatalf("schema rejects a good answer: %v", err)
	}
	if _, err := s.Parse(`{"rule":{"match":"all","conditions":[]},"summary":"x","assumptions":[],"sql":"DROP"}`); err == nil {
		t.Fatal("schema accepts an extra property")
	}
}

func TestRepairRoundFixesInvalidRule(t *testing.T) {
	bad := strings.Replace(goodAnswer, `"Istanbul"`, `"Paris"`, 1)
	fake := llmtest.New(llmtest.Text(bad), llmtest.Text(goodAnswer))
	aud, err := newComposer(fake).GenerateAudience(context.Background(), exampleBrief)
	if err != nil {
		t.Fatal(err)
	}
	if aud.Attempts != 2 {
		t.Fatalf("attempts = %d, want 2", aud.Attempts)
	}
	second := fake.Requests()[1].Messages
	if len(second) != 1 {
		t.Fatalf("repair request has %d messages; want one fresh user message", len(second))
	}
	text := second[0].Text
	if !strings.Contains(text, `unknown city \"Paris\"`) && !strings.Contains(text, `unknown city "Paris"`) {
		t.Errorf("repair request does not explain the problem:\n%s", text)
	}
	if tagged(text, "brief") != exampleBrief || !strings.Contains(tagged(text, "previous_answer"), "Paris") {
		t.Errorf("repair request lost the brief or the rejected answer")
	}
}

func TestGivesUpAfterSecondInvalidAnswer(t *testing.T) {
	fake := llmtest.New(llmtest.Text(`not json`), llmtest.Text(`{"rule":{"match":"all","conditions":[{"field":"age","op":"gt","value":30}]},"summary":"","assumptions":[]}`))
	_, err := newComposer(fake).GenerateAudience(context.Background(), exampleBrief)
	var inv *ErrInvalidOutput
	if !errors.As(err, &inv) || len(inv.Problems) == 0 || !strings.Contains(inv.Problems[0], "field") {
		t.Fatalf("err = %v", err)
	}
	if n := len(fake.Requests()); n != 2 {
		t.Fatalf("made %d calls, want 2", n)
	}
}

func TestRateLimitIsRetriedTransparently(t *testing.T) {
	fake := llmtest.New(llmtest.Fail(&llm.StatusError{Provider: "claude", StatusCode: 429}), llmtest.Text(goodAnswer))
	aud, err := newComposer(fake).GenerateAudience(context.Background(), exampleBrief)
	if err != nil || aud.Attempts != 1 {
		t.Fatalf("aud=%+v err=%v; a 429 should not count as a repair attempt", aud, err)
	}
}

func TestRefinePassesCurrentRuleAndInstruction(t *testing.T) {
	fake := llmtest.New(llmtest.Text(goodAnswer))
	current, _ := segment.Parse([]byte(`{"match":"all","conditions":[{"field":"city","op":"eq","value":"Ankara"}]}`))
	if _, err := newComposer(fake).RefineAudience(context.Background(), exampleBrief, current, "only English speakers"); err != nil {
		t.Fatal(err)
	}
	text := fake.Requests()[0].Messages[0].Text
	if !strings.Contains(tagged(text, "current_rule"), "Ankara") || tagged(text, "instruction") != "only English speakers" {
		t.Fatalf("refine prompt:\n%s", text)
	}
}

func TestMockAudience(t *testing.T) {
	cases := []struct {
		brief string
		want  string // the rule's conditions as compact JSON
	}{
		{exampleBrief, `[{"field":"city","op":"eq","value":"İstanbul"},{"field":"last_cart_at","op":"within_days","value":30},{"field":"last_purchase_at","op":"not_within_days","value":30}]`},
		{"İzmir ve Ankara'da son 14 günde sepete ürün ekleyip almayan kullanıcılara hatırlatma",
			`[{"field":"city","op":"in","value":["Ankara","İzmir"]},{"field":"last_cart_at","op":"within_days","value":14},{"field":"last_purchase_at","op":"not_within_days","value":14}]`},
		{"Thank English speaking customers who never bought anything", `[{"field":"last_purchase_at","op":"never","value":null},{"field":"language","op":"eq","value":"en"}]`},
		{"Everyone gets the new catalogue", `[]`},
	}
	for _, c := range cases {
		var streamed strings.Builder
		resp, err := (&Mock{}).Generate(context.Background(), llm.Request{Task: TaskAudience,
			Messages: []llm.Message{{Role: llm.RoleUser, Text: audiencePrompt(c.brief, nil, "")}}}, func(s string) { streamed.WriteString(s) })
		if err != nil {
			t.Fatal(err)
		}
		if streamed.String() != resp.Text {
			t.Errorf("streamed text differs from the final text")
		}
		aud, err := newComposer(llmtest.New(llmtest.Text(resp.Text))).GenerateAudience(context.Background(), c.brief)
		if err != nil {
			t.Fatalf("%q: mock answer fails validation: %v", c.brief, err)
		}
		got, _ := json.Marshal(aud.Rule.Conditions)
		if string(got) != c.want {
			t.Errorf("%q:\n got %s\nwant %s", c.brief, got, c.want)
		}
	}
}

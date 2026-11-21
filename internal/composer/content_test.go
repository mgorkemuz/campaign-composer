package composer

import (
	"context"
	"errors"
	"strings"
	"testing"

	"campaign-composer/internal/llm"
	"campaign-composer/internal/llm/llmtest"
	"campaign-composer/internal/message"
)

var longest = message.Longest{FirstName: 11, LastName: 9, City: 9}

const goodContent = `{"language":"en","sms":"Hi {{first_name}}, your cart misses you! 30% off today.",` +
	`"email_subject":"{{first_name}}, 30% off your cart","email_body":"Hi {{first_name}},\n\nYour cart is waiting.\n\nBest,\nThe team"}`

type delta struct {
	attempt     int
	field, text string
}

func generate(t *testing.T, fake *llmtest.Fake, req ContentRequest) (GeneratedContent, []delta, error) {
	t.Helper()
	var deltas []delta
	got, err := newComposer(fake).GenerateContent(context.Background(), req, func(a int, f, s string) {
		deltas = append(deltas, delta{a, f, s})
	})
	return got, deltas, err
}

func joined(deltas []delta, attempt int, field string) string {
	var b strings.Builder
	for _, d := range deltas {
		if d.attempt == attempt && d.field == field {
			b.WriteString(d.text)
		}
	}
	return b.String()
}

func TestGenerateContentStreamsFields(t *testing.T) {
	fake := llmtest.New(llmtest.Text(goodContent))
	got, deltas, err := generate(t, fake, ContentRequest{Brief: exampleBrief, AudienceSummary: "İstanbul cart abandoners", Longest: longest})
	if err != nil {
		t.Fatal(err)
	}
	if got.Attempts != 1 || got.Content.Language != "en" || got.Check.SMSLength == 0 {
		t.Fatalf("got %+v", got)
	}
	if joined(deltas, 1, "sms") != got.Content.SMS || joined(deltas, 1, "email_body") != got.Content.EmailBody {
		t.Fatal("streamed field text differs from the final content")
	}

	req := fake.Requests()[0]
	if req.Task != TaskContent || req.System != contentSystem {
		t.Errorf("task/system wrong")
	}
	langs := req.Schema["properties"].(map[string]any)["language"].(map[string]any)["enum"].([]string)
	if len(langs) != 2 {
		t.Errorf("no language chosen, but the schema allows only %v", langs)
	}
	user := req.Messages[0].Text
	if !strings.Contains(tagged(user, "limits"), "first name 11 characters") || tagged(user, "audience") == "" {
		t.Errorf("prompt lacks limits or audience:\n%s", user)
	}
}

func TestChosenLanguageIsPinned(t *testing.T) {
	fake := llmtest.New(llmtest.Text(goodContent), llmtest.Text(strings.Replace(goodContent, `"en"`, `"tr"`, 1)))
	got, _, err := generate(t, fake, ContentRequest{Brief: exampleBrief, Language: "tr", Longest: longest})
	if err != nil {
		t.Fatal(err)
	}
	if got.Attempts != 2 || got.Content.Language != "tr" {
		t.Fatalf("an English answer to a Turkish request was accepted: %+v", got)
	}
	req := fake.Requests()[0]
	if langs := req.Schema["properties"].(map[string]any)["language"].(map[string]any)["enum"].([]string); len(langs) != 1 || langs[0] != "tr" {
		t.Errorf("schema language enum = %v", langs)
	}
	if tagged(req.Messages[0].Text, "language") != "tr" {
		t.Errorf("prompt does not name the language")
	}
}

func TestRepairShortensSMS(t *testing.T) {
	long := strings.Replace(goodContent, "your cart misses you! 30% off today.", strings.Repeat("so much text ", 12), 1)
	fake := llmtest.New(llmtest.Text(long), llmtest.Text(goodContent))
	got, deltas, err := generate(t, fake, ContentRequest{Brief: exampleBrief, Longest: longest})
	if err != nil {
		t.Fatal(err)
	}
	if got.Attempts != 2 {
		t.Fatalf("attempts = %d", got.Attempts)
	}
	if joined(deltas, 1, "sms") == "" || joined(deltas, 2, "sms") != got.Content.SMS {
		t.Fatal("deltas must be tagged by attempt so the UI can start over")
	}
	if p := tagged(fake.Requests()[1].Messages[0].Text, "problems"); !strings.Contains(p, "shorten it by") {
		t.Fatalf("repair request problems = %q", p)
	}
}

func TestContentGivesUpAfterTwoBadAnswers(t *testing.T) {
	bad := strings.Replace(goodContent, "{{first_name}}, your", "{{nickname}}, your", 1)
	_, _, err := generate(t, llmtest.New(llmtest.Text(bad), llmtest.Text(bad)), ContentRequest{Brief: exampleBrief, Longest: longest})
	var inv *ErrInvalidOutput
	if !errors.As(err, &inv) || !strings.Contains(strings.Join(inv.Problems, " "), "{{nickname}}") {
		t.Fatalf("err = %v", err)
	}
}

func TestMockContentIsValid(t *testing.T) {
	cases := []struct{ brief, language, wantLang string }{
		{exampleBrief, "", "en"},
		{"İzmir'de sepetini terk eden müşterilere %20 indirim hatırlat", "", "tr"},
		{"Thank loyal customers in Ankara", "", "en"},
		{"Thank loyal customers in Ankara", "tr", "tr"},
	}
	for _, c := range cases {
		resp, err := (&Mock{}).Generate(context.Background(), llm.Request{Task: TaskContent,
			Messages: []llm.Message{{Role: llm.RoleUser, Text: contentPrompt(ContentRequest{Brief: c.brief, Language: c.language, Longest: longest})}}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		got, _, err := generate(t, llmtest.New(llmtest.Text(resp.Text)), ContentRequest{Brief: c.brief, Language: c.language, Longest: longest})
		if err != nil {
			t.Fatalf("%q: mock content invalid: %v", c.brief, err)
		}
		if got.Content.Language != c.wantLang {
			t.Errorf("%q: language %s, want %s", c.brief, got.Content.Language, c.wantLang)
		}
		if !strings.HasPrefix(resp.Text, `{"language"`) {
			t.Errorf("mock fields are not in streaming order: %.40s", resp.Text)
		}
	}
}

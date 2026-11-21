package composer

import (
	"context"
	"errors"
	"strings"
	"testing"

	"campaign-composer/internal/llm/llmtest"
	"campaign-composer/internal/message"
)

var source = message.Content{Language: "tr",
	SMS:          "Merhaba {{first_name}}, seni özledik! %15 indirim seni bekliyor.",
	EmailSubject: "{{first_name}}, seni özledik",
	EmailBody:    "Merhaba {{first_name}},\n\n{{city}}'de seni özledik.\n\nSevgiler,\nEkip"}

const goodEN = `{"language":"en","sms":"Hi {{first_name}}, we miss you! 15% off is waiting.",` +
	`"email_subject":"{{first_name}}, we miss you","email_body":"Hi {{first_name}},\n\nWe miss you in {{city}}.\n\nBest,\nThe team"}`

func TestTranslate(t *testing.T) {
	fake := llmtest.New(llmtest.Text(goodEN))
	got, err := newComposer(fake).Translate(context.Background(), TranslateRequest{Brief: "win back", Source: source, Target: "en", Longest: longest})
	if err != nil || got.Content.Language != "en" || got.Attempts != 1 {
		t.Fatalf("got %+v err %v", got, err)
	}
	req := fake.Requests()[0]
	if req.Task != TaskTranslate || tagged(req.Messages[0].Text, "language") != "en" || !strings.Contains(req.Messages[0].Text, "English") {
		t.Fatalf("request does not name the target language: %s", req.Messages[0].Text)
	}
	if langs := req.Schema["properties"].(map[string]any)["language"].(map[string]any)["enum"].([]string); len(langs) != 1 || langs[0] != "en" {
		t.Fatalf("schema language = %v", langs)
	}
}

func TestTranslateMustKeepVariables(t *testing.T) {
	dropped := strings.Replace(goodEN, "We miss you in {{city}}.", "We miss you.", 1) // lost {{city}}
	renamed := strings.Replace(goodEN, "Hi {{first_name}}, we", "Hi {{name}}, we", 1)
	fake := llmtest.New(llmtest.Text(dropped), llmtest.Text(goodEN))
	got, err := newComposer(fake).Translate(context.Background(), TranslateRequest{Source: source, Target: "en", Longest: longest})
	if err != nil || got.Attempts != 2 {
		t.Fatalf("a dropped variable must trigger the repair round: %+v %v", got, err)
	}
	if p := tagged(fake.Requests()[1].Messages[0].Text, "problems"); !strings.Contains(p, "email_body: must use exactly the variables") {
		t.Fatalf("problems = %q", p)
	}

	_, err = newComposer(llmtest.New(llmtest.Text(renamed), llmtest.Text(strings.Replace(goodEN, `"en"`, `"tr"`, 1)))).
		Translate(context.Background(), TranslateRequest{Source: source, Target: "en", Longest: longest})
	var inv *ErrInvalidOutput
	if !errors.As(err, &inv) || !strings.Contains(strings.Join(inv.Problems, " "), `language: must be "en"`) {
		t.Fatalf("err = %v", err)
	}
}

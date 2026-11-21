package composer

import (
	"encoding/json"
	"math/rand/v2"
	"strings"
	"testing"
)

// collect feeds doc in the given chunks and returns the text seen per field and
// the order fields were first seen in.
func collect(fields []string, chunks []string) (map[string]string, []string) {
	got := map[string]*strings.Builder{}
	var order []string
	fs := NewFieldStream(fields, func(field, text string) {
		if got[field] == nil {
			got[field] = &strings.Builder{}
			order = append(order, field)
		}
		got[field].WriteString(text)
	})
	for _, c := range chunks {
		fs.Write(c)
	}
	out := map[string]string{}
	for k, b := range got {
		out[k] = b.String()
	}
	return out, order
}

// A document with every awkward thing a model's JSON may contain.
const tricky = `{"language":"tr",
 "sms":"Merhaba {{first_name}}! \"Sepetin\" seni bekliyor şimdi 🛒 %30 \\ indirim\nbugün",
 "notes":["sms","not a field"],
 "email_subject":"Görüşürüz, {{first_name}}",
 "email_body":"Satır 1\n\tSatır 2 / son",
 "nested":{"sms":"wrong path"}}`

func TestFieldStreamMatchesJSONDecode(t *testing.T) {
	var want map[string]any
	if err := json.Unmarshal([]byte(tricky), &want); err != nil {
		t.Fatal(err)
	}
	fields := []string{"sms", "email_subject", "email_body"}

	check := func(name string, chunks []string) {
		got, order := collect(fields, chunks)
		for _, f := range fields {
			if got[f] != want[f] {
				t.Errorf("%s: field %s =\n  %q\nwant\n  %q", name, f, got[f], want[f])
			}
		}
		if strings.Join(order, ",") != "sms,email_subject,email_body" {
			t.Errorf("%s: fields seen in order %v", name, order)
		}
		if _, leaked := got["language"]; leaked {
			t.Errorf("%s: emitted a field nobody asked for", name)
		}
	}

	check("whole", []string{tricky})

	// One byte at a time: splits escapes, \u sequences and multi-byte characters.
	var bytes []string
	for i := range len(tricky) {
		bytes = append(bytes, tricky[i:i+1])
	}
	check("byte by byte", bytes)

	rng := rand.New(rand.NewPCG(7, 7))
	for range 200 {
		var chunks []string
		for i := 0; i < len(tricky); {
			n := 1 + rng.IntN(12)
			chunks = append(chunks, tricky[i:min(i+n, len(tricky))])
			i += n
		}
		check("random chunks", chunks)
	}
}

func TestFieldStreamNestedPath(t *testing.T) {
	got, _ := collect([]string{"email.subject"}, []string{`{"email":{"subject":"Hi`, ` there","body":"x"},"subject":"no"}`})
	if got["email.subject"] != "Hi there" || len(got) != 1 {
		t.Fatalf("got %v", got)
	}
}

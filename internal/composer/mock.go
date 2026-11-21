package composer

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"campaign-composer/internal/llm"
	"campaign-composer/internal/segment"
)

// Mock is a keyword-based stand-in for Claude, used when no API key is set, so
// the whole app can be tried offline. It answers in the same JSON shapes, and
// its answers go through the same validation as Claude's. Tests use the
// scripted llmtest.Fake instead.
type Mock struct {
	// ChunkDelay slows streaming down so the UI shows it; zero in tests.
	ChunkDelay time.Duration
}

func (m *Mock) Name() string { return "mock" }

func (m *Mock) Generate(ctx context.Context, req llm.Request, onText func(string)) (llm.Response, error) {
	var text string
	switch req.Task {
	case TaskAudience:
		last := req.Messages[len(req.Messages)-1].Text
		text = mockAudience(tagged(last, "brief") + "\n" + tagged(last, "instruction"))
	case TaskContent:
		last := req.Messages[len(req.Messages)-1].Text
		text = mockContent(tagged(last, "brief"), tagged(last, "language"))
	default:
		return llm.Response{}, fmt.Errorf("mock LLM: unknown task %q", req.Task)
	}
	// Stream in small pieces, like the real thing.
	for i := 0; i < len(text); i += 24 {
		if onText != nil {
			onText(text[i:min(i+24, len(text))])
		}
		if m.ChunkDelay > 0 {
			select {
			case <-ctx.Done():
				return llm.Response{}, ctx.Err()
			case <-time.After(m.ChunkDelay):
			}
		}
	}
	return llm.Response{Text: text, Model: "mock", StopReason: "end_turn"}, nil
}

var (
	daysRe     = regexp.MustCompile(`(\d{1,4})\s*(day|days|gün|gun)`)
	spentRe    = regexp.MustCompile(`(?:spent|spending|harcayan|harcamış)[^0-9]{0,20}(\d{2,7})`)
	cartWords  = []string{"cart", "basket", "sepet"}
	notBought  = []string{"didn't buy", "did not buy", "haven't bought", "have not bought", "not purchased", "no purchase", "without buying", "almadı", "almayan", "almamış"}
	neverWords = []string{"never bought", "never purchased", "hiç alışveriş", "hic alisveris"}
	english    = []string{"english speak", "english-speak", "ingilizce"}
)

func mockAudience(brief string) string {
	lower := strings.ToLower(brief)
	folded := foldText(brief)
	var conds []map[string]any
	var notes []string

	var cities []string
	for _, f := range segment.Fields() {
		if f.Name != "city" {
			continue
		}
		for _, c := range f.Values {
			if strings.Contains(folded, foldText(c)) {
				cities = append(cities, c)
			}
		}
	}
	switch len(cities) {
	case 0:
	case 1:
		conds = append(conds, leaf("city", "eq", cities[0]))
		notes = append(notes, "in "+cities[0])
	default:
		conds = append(conds, leaf("city", "in", cities))
		notes = append(notes, "in "+strings.Join(cities, " or "))
	}

	days := 30
	if m := daysRe.FindStringSubmatch(lower); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil && n >= 1 && n <= segment.MaxDays {
			days = n
		}
	}
	if containsAny(lower, cartWords) {
		conds = append(conds, leaf("last_cart_at", "within_days", days))
		notes = append(notes, fmt.Sprintf("who added to cart in the last %d days", days))
	}
	switch {
	case containsAny(lower, neverWords):
		conds = append(conds, leaf("last_purchase_at", "never", nil))
		notes = append(notes, "who never purchased")
	case containsAny(lower, notBought):
		conds = append(conds, leaf("last_purchase_at", "not_within_days", days))
		notes = append(notes, fmt.Sprintf("with no purchase in the last %d days", days))
	}
	if containsAny(lower, english) {
		conds = append(conds, leaf("language", "eq", "en"))
		notes = append(notes, "who speak English")
	}
	if m := spentRe.FindStringSubmatch(lower); m != nil {
		n, _ := strconv.Atoi(m[1])
		conds = append(conds, leaf("total_spent", "gt", n))
		notes = append(notes, fmt.Sprintf("who spent more than %d TRY", n))
	}

	summary := "All users."
	if len(notes) > 0 {
		summary = "Users " + strings.Join(notes, ", ") + "."
	}
	if conds == nil {
		conds = []map[string]any{}
	}
	out, _ := json.Marshal(map[string]any{
		"rule":        map[string]any{"match": "all", "conditions": conds},
		"summary":     summary,
		"assumptions": []string{"Made by the keyword mock (no ANTHROPIC_API_KEY set), not by Claude."},
	})
	return string(out)
}

func leaf(field, op string, value any) map[string]any {
	return map[string]any{"field": field, "op": op, "value": value}
}

func containsAny(s string, words []string) bool {
	for _, w := range words {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

var turkishFold = strings.NewReplacer(
	"ç", "c", "Ç", "c", "ğ", "g", "Ğ", "g", "ı", "i", "I", "i", "İ", "i",
	"ö", "o", "Ö", "o", "ş", "s", "Ş", "s", "ü", "u", "Ü", "u",
)

func foldText(s string) string { return strings.ToLower(turkishFold.Replace(s)) }

var (
	discountRe   = regexp.MustCompile(`%\s?(\d{1,2})|(\d{1,2})\s?%`)
	turkishWords = []string{" ve ", " bir ", " için ", "müşteri", "kullanıcı", "gönder", "son "}
)

// mockContent writes simple, valid messages from a few keywords in the brief.
func mockContent(brief, language string) string {
	lower := strings.ToLower(brief)
	if language == "" {
		language = "en"
		if strings.ContainsAny(brief, "çğıİöşüÇĞÖŞÜ") || containsAny(" "+lower+" ", turkishWords) {
			language = "tr"
		}
	}
	discount := ""
	if m := discountRe.FindStringSubmatch(brief); m != nil {
		discount = m[1] + m[2]
	}
	cart := containsAny(lower, cartWords)
	lapsed := !cart && containsAny(lower, notBought)

	var sms, subject, body string
	if language == "tr" {
		offer := "sana özel bir sürprizimiz var"
		if discount != "" {
			offer = "%" + discount + " indirim seni bekliyor"
		}
		if lapsed {
			sms = "Merhaba {{first_name}}, seni özledik! Son ziyaretinden bu yana yeni ürünler geldi ve " + offer + ". Uygulamaya göz at."
			subject = "{{first_name}}, seni yeniden görmek isteriz"
			body = "Merhaba {{first_name}},\n\nBir süredir uğramadın; bu arada senin için yeni ürünler ekledik. Üstelik " + offer + ".\n\nNeler değiştiğini görmek için uygulamayı açman yeterli.\n\nSevgiler,\nEkip"
		} else if cart {
			sms = "Merhaba {{first_name}}, sepetindeki ürünler hâlâ seni bekliyor ve " + offer + "! Alışverişini bugün tamamla."
			subject = "{{first_name}}, sepetin seni bekliyor"
			body = "Merhaba {{first_name}},\n\nSepetine eklediğin ürünler hâlâ seni bekliyor. Üstelik " + offer + ".\n\nAlışverişini tamamlamak için uygulamaya göz atman yeterli.\n\nSevgiler,\nEkip"
		} else {
			sms = "Merhaba {{first_name}}, " + offer + "! Detaylar için uygulamaya göz at."
			subject = "{{first_name}}, " + offer
			body = "Merhaba {{first_name}},\n\nBizi tercih ettiğin için teşekkür ederiz. Bu yüzden " + offer + ".\n\nDetaylar için uygulamaya göz atabilirsin.\n\nSevgiler,\nEkip"
		}
	} else {
		offer := "we have a little surprise for you"
		if discount != "" {
			offer = discount + "% off is waiting for you"
		}
		if lapsed {
			sms = "Hi {{first_name}}, we've missed you! There's plenty new since your last visit and " + offer + ". Open the app to take a look."
			subject = "{{first_name}}, come see what's new"
			body = "Hi {{first_name}},\n\nIt's been a while, and a lot has changed since your last visit. And " + offer + ".\n\nOpen the app to see what's new.\n\nBest,\nThe team"
		} else if cart {
			sms = "Hi {{first_name}}, the items in your cart are still waiting and " + offer + "! Finish your order today."
			subject = "{{first_name}}, your cart is waiting"
			body = "Hi {{first_name}},\n\nThe items you added to your cart are still waiting for you. And " + offer + ".\n\nOpen the app to finish your order.\n\nBest,\nThe team"
		} else {
			sms = "Hi {{first_name}}, " + offer + "! Open the app for the details."
			subject = "{{first_name}}, " + offer
			body = "Hi {{first_name}},\n\nThank you for shopping with us. That is why " + offer + ".\n\nOpen the app for the details.\n\nBest,\nThe team"
		}
	}
	// A struct, not a map, so fields stream in the same order as Claude's.
	out, _ := json.Marshal(struct {
		Language     string `json:"language"`
		SMS          string `json:"sms"`
		EmailSubject string `json:"email_subject"`
		EmailBody    string `json:"email_body"`
	}{language, sms, subject, body})
	return string(out)
}

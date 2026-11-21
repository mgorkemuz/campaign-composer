package message

import (
	"strings"
	"testing"
)

var longest = Longest{FirstName: 11, LastName: 9, City: 9} // "Abdurrahman", "Karaoğlu.", "Eskişehir"

func TestRender(t *testing.T) {
	r := Recipient{FirstName: "Ayşe", LastName: "Yılmaz", City: "İzmir"}
	got := Render("Merhaba {{first_name}} {{ last_name }}, {{city}}'de seni bekliyoruz!", r)
	if got != "Merhaba Ayşe Yılmaz, İzmir'de seni bekliyoruz!" {
		t.Fatalf("got %q", got)
	}
	if got := Render("Hi {{nickname}}", r); got != "Hi {{nickname}}" {
		t.Fatalf("unknown variable rendered as %q; validation should have stopped it", got)
	}
}

func TestWorstCaseLength(t *testing.T) {
	if n := WorstCaseLength("Hi {{first_name}}!", longest); n != len("Hi !")+11 {
		t.Fatalf("n = %d", n)
	}
	if n := WorstCaseLength("Şehir: {{city}}", longest); n != 7+9 {
		t.Fatalf("n = %d; must count characters, not bytes", n)
	}
}

func valid() Content {
	return Content{Language: "tr", SMS: "Merhaba {{first_name}}, sepetin seni bekliyor! %30 indirim bugün.",
		EmailSubject: "{{first_name}}, sepetindekiler hâlâ burada", EmailBody: "Merhaba {{first_name}},\n\nSepetin seni bekliyor."}
}

func TestValidateAcceptsGoodContent(t *testing.T) {
	chk, problems := Validate(valid(), longest)
	if len(problems) != 0 {
		t.Fatalf("problems: %v", problems)
	}
	if chk.SMSEncoding != "GSM-7" && chk.SMSEncoding != "UCS-2" || chk.SMSSegments < 1 || chk.SMSLength <= 0 {
		t.Fatalf("check = %+v", chk)
	}
}

func TestValidateProblems(t *testing.T) {
	cases := []struct {
		name    string
		edit    func(*Content)
		mention string
	}{
		{"unknown variable", func(c *Content) { c.SMS = "Hi {{nickname}}" }, "unknown variable {{nickname}}"},
		{"unbalanced braces", func(c *Content) { c.EmailBody = "Hi {{first_name}" }, "unbalanced"},
		{"empty", func(c *Content) { c.EmailSubject = "  " }, "email_subject: is empty"},
		{"multi-line subject", func(c *Content) { c.EmailSubject = "Hi\nthere" }, "single line"},
		{"bad language", func(c *Content) { c.Language = "de" }, "language"},
		// 150 literal characters + an 11-character name = 161.
		{"sms too long once personalized", func(c *Content) { c.SMS = strings.Repeat("a", 150) + "{{first_name}}" }, "shorten it by 1"},
		{"subject too long", func(c *Content) { c.EmailSubject = strings.Repeat("b", 81) }, "email_subject: up to 81"},
	}
	for _, c := range cases {
		content := valid()
		c.edit(&content)
		_, problems := Validate(content, longest)
		if !strings.Contains(strings.Join(problems, "\n"), c.mention) {
			t.Errorf("%s: problems %q do not mention %q", c.name, problems, c.mention)
		}
	}
}

func TestSMSOfExactlyTheLimitPasses(t *testing.T) {
	c := valid()
	c.SMS = strings.Repeat("a", 149) + "{{first_name}}" // 149 + 11 = 160
	if _, problems := Validate(c, longest); len(problems) != 0 {
		t.Fatalf("a 160-character SMS was rejected: %v", problems)
	}
}

func TestSMSSegments(t *testing.T) {
	cases := []struct {
		text     string
		encoding string
		segments int
	}{
		{strings.Repeat("a", 160), "GSM-7", 1},
		{strings.Repeat("a", 161), "GSM-7", 2},
		{strings.Repeat("a", 306), "GSM-7", 2},
		{strings.Repeat("a", 307), "GSM-7", 3},
		{strings.Repeat("€", 80), "GSM-7", 1}, // extension characters count twice
		{strings.Repeat("€", 81), "GSM-7", 2},
		{"Görüşürüz", "UCS-2", 1}, // ş is not GSM-7 (ö and ü are)
		{"Çok güzel üzüm", "GSM-7", 1},
		{strings.Repeat("ş", 70), "UCS-2", 1},
		{strings.Repeat("ş", 71), "UCS-2", 2},
		{"", "GSM-7", 0},
	}
	for _, c := range cases {
		enc, seg := SMSSegments(c.text)
		if enc != c.encoding || seg != c.segments {
			t.Errorf("%q (%d chars): %s/%d, want %s/%d", c.text[:min(len(c.text), 12)], len([]rune(c.text)), enc, seg, c.encoding, c.segments)
		}
	}
}

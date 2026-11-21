// Package message checks and renders message templates: SMS text and email
// subject/body with {{variable}} personalization.
package message

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// Variables are the only placeholders a template may use.
var Variables = []string{"first_name", "last_name", "city"}

const (
	SMSMaxChars       = 160
	SubjectMaxChars   = 80
	EmailBodyMaxChars = 5000
)

var Languages = []string{"tr", "en"}

// Content is one campaign's messages, in one language.
type Content struct {
	Language     string `json:"language"`
	SMS          string `json:"sms"`
	EmailSubject string `json:"email_subject"`
	EmailBody    string `json:"email_body"`
}

// Recipient holds the values the variables are filled with.
type Recipient struct {
	FirstName string
	LastName  string
	City      string
}

func (r Recipient) value(name string) string {
	switch name {
	case "first_name":
		return r.FirstName
	case "last_name":
		return r.LastName
	case "city":
		return r.City
	}
	return ""
}

// Longest holds the longest value of each variable in an audience, in
// characters. The length limits are checked against them, so no recipient's
// message goes over.
type Longest struct {
	FirstName int `json:"first_name"`
	LastName  int `json:"last_name"`
	City      int `json:"city"`
}

func (l Longest) of(name string) int {
	switch name {
	case "first_name":
		return l.FirstName
	case "last_name":
		return l.LastName
	case "city":
		return l.City
	}
	return 0
}

var varRe = regexp.MustCompile(`\{\{\s*([^{}]*?)\s*\}\}`)

// checkVariables reports unknown variables and stray braces.
func checkVariables(field, text string) []string {
	var problems []string
	for _, m := range varRe.FindAllStringSubmatch(text, -1) {
		if !slices.Contains(Variables, m[1]) {
			problems = append(problems, fmt.Sprintf("%s: unknown variable {{%s}}; use only %s", field, m[1], allowedVars()))
		}
	}
	rest := varRe.ReplaceAllString(text, "")
	if strings.Contains(rest, "{{") || strings.Contains(rest, "}}") {
		problems = append(problems, fmt.Sprintf("%s: has unbalanced {{ }}; write variables as {{first_name}}", field))
	}
	return problems
}

func allowedVars() string {
	parts := make([]string, len(Variables))
	for i, v := range Variables {
		parts[i] = "{{" + v + "}}"
	}
	return strings.Join(parts, ", ")
}

// Render fills the variables for one recipient.
func Render(text string, r Recipient) string {
	return varRe.ReplaceAllStringFunc(text, func(m string) string {
		name := varRe.FindStringSubmatch(m)[1]
		if !slices.Contains(Variables, name) {
			return m
		}
		return r.value(name)
	})
}

// WorstCaseLength is the length in characters after rendering for the
// recipient with the longest values.
func WorstCaseLength(text string, l Longest) int {
	n := utf8.RuneCountInString(varRe.ReplaceAllString(text, ""))
	for _, m := range varRe.FindAllStringSubmatch(text, -1) {
		n += l.of(m[1])
	}
	return n
}

// Check describes content against the audience it will be sent to.
type Check struct {
	SMSLength     int    `json:"sms_length"`     // worst case after personalization
	SubjectLength int    `json:"subject_length"` // worst case after personalization
	SMSEncoding   string `json:"sms_encoding"`   // "GSM-7" or "UCS-2", worst case
	SMSSegments   int    `json:"sms_segments"`   // parts the longest SMS is split into
}

// Validate checks content against the limits and returns every problem, so one
// repair round (or one edit) can fix them all.
func Validate(c Content, l Longest) (Check, []string) {
	var problems []string
	if !slices.Contains(Languages, c.Language) {
		problems = append(problems, fmt.Sprintf("language: must be one of %s", strings.Join(Languages, ", ")))
	}
	fields := []struct{ name, text string }{
		{"sms", c.SMS}, {"email_subject", c.EmailSubject}, {"email_body", c.EmailBody},
	}
	for _, f := range fields {
		if strings.TrimSpace(f.text) == "" {
			problems = append(problems, f.name+": is empty")
			continue
		}
		problems = append(problems, checkVariables(f.name, f.text)...)
	}
	if strings.ContainsAny(c.EmailSubject, "\r\n") {
		problems = append(problems, "email_subject: must be a single line")
	}

	chk := Check{
		SMSLength:     WorstCaseLength(c.SMS, l),
		SubjectLength: WorstCaseLength(c.EmailSubject, l),
	}
	if chk.SMSLength > SMSMaxChars {
		problems = append(problems, fmt.Sprintf(
			"sms: up to %d characters once personalized (longest names in this audience), limit %d; shorten it by %d",
			chk.SMSLength, SMSMaxChars, chk.SMSLength-SMSMaxChars))
	}
	if chk.SubjectLength > SubjectMaxChars {
		problems = append(problems, fmt.Sprintf(
			"email_subject: up to %d characters once personalized, limit %d; shorten it by %d",
			chk.SubjectLength, SubjectMaxChars, chk.SubjectLength-SubjectMaxChars))
	}
	if n := utf8.RuneCountInString(c.EmailBody); n > EmailBodyMaxChars {
		problems = append(problems, fmt.Sprintf("email_body: %d characters, limit %d", n, EmailBodyMaxChars))
	}
	chk.SMSEncoding, chk.SMSSegments = SMSSegments(worstCaseText(c.SMS, l))
	return chk, problems
}

// worstCaseText renders with placeholder letters of the longest lengths. They
// are GSM-7 letters, so the encoding reflects the template's own characters.
func worstCaseText(text string, l Longest) string {
	return varRe.ReplaceAllStringFunc(text, func(m string) string {
		return strings.Repeat("x", l.of(varRe.FindStringSubmatch(m)[1]))
	})
}

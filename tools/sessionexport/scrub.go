package main

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
)

// Rule is a literal find/replace applied to every string in the transcript.
type Rule struct{ Find, Replace string }

// Scrubber removes personal data from transcript strings and checks the result for leaks.
// Private terms (home dir, username, project names) are never hard-coded: they come from
// the running machine and from a gitignored rules file.
type Scrubber struct {
	rules []Rule
}

var (
	emailRe = regexp.MustCompile(`[A-Za-z0-9._%+-]+@([A-Za-z0-9-]+\.)+[A-Za-z]{2,}`)
	// The app attaches a list of recently used folders on this machine; none of it belongs in the export.
	recentFoldersRe = regexp.MustCompile(`(?s)<recent-project-folders>.*?</recent-project-folders>`)

	// Domains that are safe to keep (commit trailers, docs examples).
	keepEmailDomains = []string{"anthropic.com", "example.com", "example.org", "users.noreply.github.com"}

	// Credential-shaped strings. A hit fails the export; values are never printed.
	secretPatterns = map[string]*regexp.Regexp{
		"anthropic key":    regexp.MustCompile(`sk-ant-[A-Za-z0-9_-]{10,}`),
		"openai-style key": regexp.MustCompile(`\bsk-[A-Za-z0-9]{20,}`),
		"google api key":   regexp.MustCompile(`AIza[0-9A-Za-z_-]{30,}`),
		"aws access key":   regexp.MustCompile(`AKIA[0-9A-Z]{16}`),
		"github token":     regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{30,}`),
		"stripe key":       regexp.MustCompile(`(?:sk|rk)_(?:live|test)_[A-Za-z0-9]{10,}`),
		"jwt":              regexp.MustCompile(`eyJ[A-Za-z0-9_-]{15,}\.eyJ[A-Za-z0-9_-]{10,}`),
		"private key":      regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY`),
	}
)

// NewScrubber builds a scrubber. home and username come from the current machine; extra
// rules come from the rules file. Longer finds are applied first so specific rules win.
func NewScrubber(home, username string, extra []Rule) *Scrubber {
	var rules []Rule
	if home != "" && home != "/" {
		rules = append(rules, Rule{home, "~"})
	}
	if len(username) >= 4 { // shorter names would mangle ordinary words
		rules = append(rules, Rule{username, "<user>"})
	}
	sort.SliceStable(extra, func(i, j int) bool { return len(extra[i].Find) > len(extra[j].Find) })
	rules = append(rules, extra...)
	return &Scrubber{rules: rules}
}

// String scrubs one string. Order matters: emails go before the username rule so
// "name@host" is still recognisable as an email when it is replaced.
func (s *Scrubber) String(in string) string {
	out := recentFoldersRe.ReplaceAllString(in, "<recent-project-folders>[removed]</recent-project-folders>")
	out = emailRe.ReplaceAllStringFunc(out, func(m string) string {
		if keepEmail(m) {
			return m
		}
		return "<email>"
	})
	for _, r := range s.rules {
		out = strings.ReplaceAll(out, r.Find, r.Replace)
	}
	return out
}

// Value scrubs every string inside a decoded JSON value.
func (s *Scrubber) Value(v any) any {
	switch t := v.(type) {
	case string:
		return s.String(t)
	case []any:
		for i := range t {
			t[i] = s.Value(t[i])
		}
		return t
	case map[string]any:
		for k := range t {
			t[k] = s.Value(t[k])
		}
		return t
	default:
		return v
	}
}

// Leak is one suspicious match in the scrubbed output. It names the kind and line only.
type Leak struct {
	Kind string
	Line int
}

func (l Leak) String() string { return fmt.Sprintf("line %d: %s", l.Line, l.Kind) }

// Leaks scans scrubbed text for anything that should not have survived: credential
// patterns, foreign emails, and any rule's find text in any letter case.
func (s *Scrubber) Leaks(text string) []Leak {
	var leaks []Leak
	for i, line := range strings.Split(text, "\n") {
		n := i + 1
		for kind, re := range secretPatterns {
			if re.MatchString(line) {
				leaks = append(leaks, Leak{kind, n})
			}
		}
		for _, m := range emailRe.FindAllString(line, -1) {
			if !keepEmail(m) {
				leaks = append(leaks, Leak{"email address", n})
			}
		}
		lower := strings.ToLower(line)
		for i, r := range s.rules {
			if strings.Contains(lower, strings.ToLower(r.Find)) {
				leaks = append(leaks, Leak{fmt.Sprintf("scrub rule #%d text", i+1), n})
			}
		}
	}
	sort.Slice(leaks, func(i, j int) bool { return leaks[i].Line < leaks[j].Line })
	return leaks
}

func keepEmail(addr string) bool {
	lower := strings.ToLower(addr)
	for _, d := range keepEmailDomains {
		if strings.HasSuffix(lower, "@"+d) || strings.HasSuffix(lower, "."+d) {
			return true
		}
	}
	return false
}

// LoadRules reads "find => replace" lines; blank lines and # comments are skipped.
// A missing file is not an error.
func LoadRules(path string) ([]Rule, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var rules []Rule
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		find, repl, ok := strings.Cut(line, "=>")
		find, repl = strings.TrimSpace(find), strings.TrimSpace(repl)
		if !ok || find == "" {
			return nil, fmt.Errorf("%s:%d: want \"find => replace\"", path, n)
		}
		rules = append(rules, Rule{find, repl})
	}
	return rules, sc.Err()
}

package main

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testScrubber() *Scrubber {
	return NewScrubber("/Users/alice", "alice", []Rule{
		{"secret-proj", "project-b"},
		{"secret-proj-v2", "project-c"}, // longer rule must win
	})
}

func TestScrubString(t *testing.T) {
	s := testScrubber()
	cases := []struct{ name, in, want string }{
		{"home dir", "open /Users/alice/Documents/app/main.go", "open ~/Documents/app/main.go"},
		{"flattened home", "-Users-alice-Library-x", "-Users-<user>-Library-x"},
		{"user email", "mail alice@uni.edu.tr now", "mail <email> now"},
		{"email before username", "alice@uni.edu", "<email>"},
		{"kept email", "Co-Authored-By: Claude <noreply@anthropic.com>", "Co-Authored-By: Claude <noreply@anthropic.com>"},
		{"extra rule", "see ~/Documents/secret-proj", "see ~/Documents/project-b"},
		{"longest rule first", "secret-proj-v2", "project-c"},
		{"recent folders", "a<recent-project-folders>\n- /x/y\n</recent-project-folders>b",
			"a<recent-project-folders>[removed]</recent-project-folders>b"},
	}
	for _, c := range cases {
		if got := s.String(c.in); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestShortUsernameIsNotARule(t *testing.T) {
	s := NewScrubber("", "al", nil)
	if got := s.String("total"); got != "total" {
		t.Fatalf("short username mangled text: %q", got)
	}
}

func TestLeaks(t *testing.T) {
	s := testScrubber()
	clean := "nothing to see\nCo-Authored-By: Claude <noreply@anthropic.com>"
	if l := s.Leaks(clean); len(l) != 0 {
		t.Fatalf("clean text flagged: %v", l)
	}
	dirty := strings.Join([]string{
		"ok",
		"key sk-ant-" + strings.Repeat("a", 20),
		"from bob@corp.io",
		"Secret-Proj in another case",
	}, "\n")
	got := s.Leaks(dirty)
	want := map[int]bool{2: true, 3: true, 4: true}
	if len(got) != len(want) {
		t.Fatalf("got %d leaks %v, want lines 2,3,4", len(got), got)
	}
	for _, l := range got {
		if !want[l.Line] {
			t.Errorf("unexpected leak %v", l)
		}
		if strings.Contains(l.String(), "aaaa") {
			t.Errorf("leak report contains the secret value: %v", l)
		}
	}
}

func TestLoadRules(t *testing.T) {
	p := filepath.Join(t.TempDir(), "rules")
	os.WriteFile(p, []byte("# comment\n\nfoo => bar\n  baz=>qux  \n"), 0o644)
	rules, err := LoadRules(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 2 || rules[1] != (Rule{"baz", "qux"}) {
		t.Fatalf("rules = %+v", rules)
	}

	os.WriteFile(p, []byte("no arrow here\n"), 0o644)
	if _, err := LoadRules(p); err == nil {
		t.Fatal("want error for a malformed line")
	}
	if rules, err := LoadRules(filepath.Join(t.TempDir(), "missing")); err != nil || rules != nil {
		t.Fatalf("missing file: rules=%v err=%v", rules, err)
	}
}

const fixture = `{"type":"user","uuid":"u1","timestamp":"2026-11-20T17:31:18Z","message":{"role":"user","content":"<system-reminder>email alice@uni.edu</system-reminder>hi, read /Users/alice/secret-proj"}}
{"type":"api-request-blob","blob":"huge internal payload"}
{"type":"attachment","attachment":{"type":"environment"}}
{"type":"assistant","uuid":"a1","timestamp":"2026-11-20T17:31:30Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":"plan it","signature":"SIG"},{"type":"text","text":"Reading it."},{"type":"tool_use","id":"t1","name":"Bash","caller":{"x":1},"input":{"command":"ls /Users/alice","description":"List files"}}]}}
{"type":"user","uuid":"u2","timestamp":"2026-11-20T17:31:31Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","is_error":false,"content":[{"type":"text","text":"main.go"}]}]}}
{"type":"user","uuid":"u3","isMeta":true,"timestamp":"2026-11-20T17:31:32Z","message":{"role":"user","content":"skill body"}}
{"type":"user","uuid":"u4","isMeta":true,"origin":{"kind":"peer","name":"Explore","body":"DUPLICATE BODY"},"timestamp":"2026-11-20T17:32:00Z","message":{"role":"user","content":"Survey done: use pgx"}}
`

func writeZip(t *testing.T, files map[string]string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "export.zip")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, body := range files {
		w, _ := zw.Create(name)
		w.Write([]byte(body))
	}
	zw.Close()
	f.Close()
	return p
}

func TestPipeline(t *testing.T) {
	zipPath := writeZip(t, map[string]string{
		"transcript.jsonl":             fixture,
		"metadata.json":                `{"secret":"x"}`,
		"sess/subagents/agent-1.jsonl": `{"type":"user"}`,
	})
	raw, err := ReadTranscript(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	rows := Prune(raw)
	if len(rows) != 5 {
		t.Fatalf("kept %d rows, want 5 (user/assistant only)", len(rows))
	}
	s := testScrubber()
	for _, r := range rows {
		s.Value(map[string]any(r))
	}
	jsonl, err := EncodeJSONL(rows)
	if err != nil {
		t.Fatal(err)
	}
	md := RenderMarkdown("Session test", rows)

	for _, out := range []string{string(jsonl), md} {
		for _, bad := range []string{"alice", "secret-proj", "SIG", "huge internal payload", `"caller"`, "DUPLICATE BODY"} {
			if strings.Contains(out, bad) {
				t.Errorf("output still contains %q", bad)
			}
		}
		if l := s.Leaks(out); len(l) != 0 {
			t.Errorf("leaks in output: %v", l)
		}
	}
	for _, want := range []string{"## User · 2026-11-20 17:31 UTC", "hi, read ~/project-b", "## Claude", "Tool: Bash — List files", "main.go", "plan it", "## Subagent report (Explore) · 2026-11-20 17:32 UTC", "Survey done: use pgx"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown missing %q", want)
		}
	}
	if strings.Contains(md, "skill body") || strings.Contains(md, "system-reminder") {
		t.Error("markdown should hide meta rows and system reminders")
	}
}

func TestTruncationAndFences(t *testing.T) {
	var b strings.Builder
	details(&b, "x", "text", strings.Repeat("é", 10)+"```", 5)
	out := b.String()
	if !strings.Contains(out, "[truncated: 5 of 13 characters shown]") {
		t.Fatalf("no truncation note: %s", out)
	}
	if got := fenceFor("a ``` b"); got != "````" {
		t.Fatalf("fenceFor = %q", got)
	}
}

func TestReadTranscriptMissing(t *testing.T) {
	if _, err := ReadTranscript(writeZip(t, map[string]string{"other.jsonl": "{}"})); err == nil {
		t.Fatal("want error when transcript.jsonl is absent")
	}
}

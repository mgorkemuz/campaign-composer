// Command sessionexport turns a Claude app session export (zip) into a scrubbed
// transcript for ai-sessions/: a full .jsonl record and a readable .md view.
//
//	go run ./tools/sessionexport -in ~/Downloads/session-export-123.zip -name 01-scaffold
//
// Personal data is removed with machine-derived rules (home dir, username), an email
// filter, and extra "find => replace" rules from a gitignored file (default .scrub.local).
// The export fails, writing nothing, if anything credential-shaped or private survives.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"time"
)

func main() {
	in := flag.String("in", "", "session export zip from the Claude app (required)")
	out := flag.String("out", "ai-sessions", "output directory")
	name := flag.String("name", "", "base file name, e.g. 01-scaffold (default: date of the session)")
	rulesPath := flag.String("rules", ".scrub.local", "extra scrub rules file, one \"find => replace\" per line")
	flag.Parse()

	if *in == "" {
		flag.Usage()
		os.Exit(2)
	}
	if err := run(*in, *out, *name, *rulesPath); err != nil {
		fmt.Fprintln(os.Stderr, "sessionexport:", err)
		os.Exit(1)
	}
}

func run(in, outDir, name, rulesPath string) error {
	extra, err := LoadRules(rulesPath)
	if err != nil {
		return err
	}
	home, _ := os.UserHomeDir()
	username := ""
	if u, err := user.Current(); err == nil {
		username = u.Username
	}
	s := NewScrubber(home, username, extra)

	raw, err := ReadTranscript(in)
	if err != nil {
		return err
	}
	rows := Prune(raw)
	if len(rows) == 0 {
		return errors.New("no conversation rows in the transcript")
	}
	for _, r := range rows {
		s.Value(map[string]any(r))
	}

	if name == "" {
		name = sessionDate(rows) + "-session"
	}
	jsonl, err := EncodeJSONL(rows)
	if err != nil {
		return err
	}
	md := RenderMarkdown("Session "+name, rows)

	if leaks := append(tag("jsonl", s.Leaks(string(jsonl))), tag("md", s.Leaks(md))...); len(leaks) > 0 {
		for _, l := range leaks {
			fmt.Fprintln(os.Stderr, "  leak:", l)
		}
		return fmt.Errorf("%d possible leaks; add rules to %s and run again (nothing was written)", len(leaks), rulesPath)
	}

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	jsonlPath := filepath.Join(outDir, name+".jsonl")
	mdPath := filepath.Join(outDir, name+".md")
	if err := os.WriteFile(jsonlPath, jsonl, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(mdPath, []byte(md), 0o644); err != nil {
		return err
	}
	fmt.Printf("wrote %s (%d rows) and %s\n", jsonlPath, len(rows), mdPath)
	return nil
}

func sessionDate(rows []Row) string {
	for _, r := range rows {
		if ts, ok := r["timestamp"].(string); ok {
			if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
				return t.UTC().Format("2006-01-02")
			}
		}
	}
	return time.Now().UTC().Format("2006-01-02")
}

type fileLeak struct {
	file string
	Leak
}

func (l fileLeak) String() string { return l.file + " " + l.Leak.String() }

func tag(file string, leaks []Leak) []fmt.Stringer {
	out := make([]fmt.Stringer, len(leaks))
	for i, l := range leaks {
		out[i] = fileLeak{file, l}
	}
	return out
}

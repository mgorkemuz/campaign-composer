package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxToolInput  = 4000
	maxToolResult = 3000
)

// App-injected context in user turns; useful in the raw JSONL, noise in the readable view.
var reminderRe = regexp.MustCompile(`(?s)<system-reminder>.*?</system-reminder>`)

// RenderMarkdown turns pruned, scrubbed rows into a readable conversation.
// The JSONL next to it stays the complete record; this view truncates long tool I/O.
func RenderMarkdown(title string, rows []Row) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", title)
	b.WriteString("Scrubbed export of a Claude Code session. Tool inputs and outputs are truncated here; the `.jsonl` file next to this one has them in full.\n")

	speaker := ""
	heading := func(who string, row Row) {
		if who == speaker {
			return
		}
		speaker = who
		fmt.Fprintf(&b, "\n---\n\n## %s%s\n\n", who, stamp(row))
	}

	for _, row := range rows {
		msg, _ := row["message"].(map[string]any)
		if name, ok := peerName(row); ok {
			if text, _ := msg["content"].(string); cleanUserText(text) != "" {
				speaker = "" // force a fresh heading after the report
				fmt.Fprintf(&b, "\n---\n\n## Subagent report (%s)%s\n\n%s\n\n", name, stamp(row), cleanUserText(text))
			}
			continue
		}
		if row["isMeta"] == true {
			continue // skill bodies and other app-injected context
		}
		switch c := msg["content"].(type) {
		case string:
			if text := cleanUserText(c); text != "" {
				heading(who(row), row)
				b.WriteString(text + "\n\n")
			}
		case []any:
			for _, raw := range c {
				blk, _ := raw.(map[string]any)
				switch blk["type"] {
				case "text":
					text, _ := blk["text"].(string)
					if row["type"] == "user" {
						text = cleanUserText(text)
					}
					if strings.TrimSpace(text) != "" {
						heading(who(row), row)
						b.WriteString(strings.TrimSpace(text) + "\n\n")
					}
				case "thinking":
					if t, _ := blk["thinking"].(string); strings.TrimSpace(t) != "" {
						heading("Claude", row)
						details(&b, "Thinking", "text", t, 0)
					}
				case "tool_use":
					heading("Claude", row)
					name, _ := blk["name"].(string)
					in, _ := blk["input"].(map[string]any)
					label := "Tool: " + name
					if d, _ := in["description"].(string); d != "" {
						label += " — " + d
					}
					js, _ := json.MarshalIndent(in, "", "  ")
					details(&b, label, "json", string(js), maxToolInput)
				case "tool_result":
					label := "Result"
					if blk["is_error"] == true {
						label = "Result (error)"
					}
					details(&b, label, "text", resultText(blk["content"]), maxToolResult)
				}
			}
		}
	}
	return b.String()
}

func peerName(row Row) (string, bool) {
	o, _ := row["origin"].(map[string]any)
	if o == nil || o["kind"] != "peer" {
		return "", false
	}
	name, _ := o["name"].(string)
	if name == "" {
		name = "agent"
	}
	return name, true
}

func who(row Row) string {
	if row["type"] == "assistant" {
		return "Claude"
	}
	return "User"
}

func stamp(row Row) string {
	ts, _ := row["timestamp"].(string)
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return ""
	}
	return " · " + t.UTC().Format("2006-01-02 15:04 UTC")
}

func cleanUserText(s string) string {
	return strings.TrimSpace(reminderRe.ReplaceAllString(s, ""))
}

func resultText(c any) string {
	switch t := c.(type) {
	case string:
		return t
	case []any:
		var parts []string
		for _, raw := range t {
			if m, ok := raw.(map[string]any); ok {
				if s, ok := m["text"].(string); ok {
					parts = append(parts, s)
				}
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

// details writes a collapsed block. limit 0 means no truncation.
func details(b *strings.Builder, summary, lang, body string, limit int) {
	body = strings.TrimRight(body, "\n")
	if limit > 0 && utf8.RuneCountInString(body) > limit {
		total := utf8.RuneCountInString(body)
		body = string([]rune(body)[:limit]) + fmt.Sprintf("\n… [truncated: %d of %d characters shown]", limit, total)
	}
	fence := fenceFor(body)
	fmt.Fprintf(b, "<details><summary>%s</summary>\n\n%s%s\n%s\n%s\n\n</details>\n\n",
		escapeHTML(summary), fence, lang, body, fence)
}

// fenceFor returns a backtick fence longer than any backtick run inside body.
func fenceFor(body string) string {
	longest, run := 0, 0
	for _, r := range body {
		if r == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	return strings.Repeat("`", max(3, longest+1))
}

func escapeHTML(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

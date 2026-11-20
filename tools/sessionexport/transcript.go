package main

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
)

// Row is one kept transcript entry, reduced to what a reader needs.
type Row map[string]any

// ReadTranscript returns the rows of transcript.jsonl from a Claude app session export.
// Subagent transcripts, metadata and app state in the zip are deliberately ignored.
func ReadTranscript(zipPath string) ([]map[string]any, error) {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return nil, err
	}
	defer zr.Close()

	for _, f := range zr.File {
		if path.Clean(f.Name) != "transcript.jsonl" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		return decodeRows(rc)
	}
	return nil, errors.New("transcript.jsonl not found at the root of the zip")
}

func decodeRows(r io.Reader) ([]map[string]any, error) {
	dec := json.NewDecoder(r) // streams; some lines are megabytes long
	var rows []map[string]any
	for {
		var row map[string]any
		err := dec.Decode(&row)
		if errors.Is(err, io.EOF) {
			return rows, nil
		}
		if err != nil {
			return nil, fmt.Errorf("row %d: %w", len(rows)+1, err)
		}
		rows = append(rows, row)
	}
}

// Prune keeps only conversation rows (user and assistant) and only the fields that carry
// the conversation. App bookkeeping (request blobs, attachments, permission state,
// thinking signatures) is dropped.
func Prune(rows []map[string]any) []Row {
	var out []Row
	for _, r := range rows {
		typ, _ := r["type"].(string)
		if typ != "user" && typ != "assistant" {
			continue
		}
		msg, _ := r["message"].(map[string]any)
		if msg == nil {
			continue
		}
		kept := Row{
			"type":      typ,
			"uuid":      r["uuid"],
			"timestamp": r["timestamp"],
			"message":   map[string]any{"role": msg["role"], "content": pruneContent(msg["content"])},
		}
		if p, ok := r["parentUuid"]; ok {
			kept["parentUuid"] = p
		}
		if meta, _ := r["isMeta"].(bool); meta {
			kept["isMeta"] = true
		}
		// Subagent reports arrive as meta rows with a "peer" origin. Keep who sent it,
		// not the origin's copy of the body (it duplicates message.content).
		if o, _ := r["origin"].(map[string]any); o != nil && o["kind"] == "peer" {
			kept["origin"] = map[string]any{"kind": "peer", "name": o["name"]}
		}
		out = append(out, kept)
	}
	return out
}

func pruneContent(c any) any {
	blocks, ok := c.([]any)
	if !ok {
		return c // plain string
	}
	out := make([]any, 0, len(blocks))
	for _, b := range blocks {
		m, ok := b.(map[string]any)
		if !ok {
			continue
		}
		switch m["type"] {
		case "thinking":
			out = append(out, map[string]any{"type": "thinking", "thinking": m["thinking"]})
		case "redacted_thinking":
			// opaque, nothing a reader can use
		case "tool_use":
			out = append(out, map[string]any{"type": "tool_use", "id": m["id"], "name": m["name"], "input": m["input"]})
		case "tool_result":
			out = append(out, map[string]any{
				"type": "tool_result", "tool_use_id": m["tool_use_id"],
				"is_error": m["is_error"], "content": pruneContent(m["content"]),
			})
		case "image":
			out = append(out, map[string]any{"type": "text", "text": "[image omitted]"})
		default:
			out = append(out, m)
		}
	}
	return out
}

// EncodeJSONL writes rows one per line.
func EncodeJSONL(rows []Row) ([]byte, error) {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	for _, r := range rows {
		if err := enc.Encode(r); err != nil {
			return nil, err
		}
	}
	return []byte(b.String()), nil
}

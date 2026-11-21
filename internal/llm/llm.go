// Package llm is the provider-neutral model interface, ported from agents-platform
// and narrowed to what this app needs: one system prompt, a short conversation,
// a JSON schema the answer must follow, and streamed text.
package llm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

type Message struct {
	Role Role   `json:"role"`
	Text string `json:"text"`
}

type Request struct {
	// Task names the job ("audience", "content"). Logs and metrics use it, and
	// the keyword mock uses it to know what to answer.
	Task     string
	System   string
	Messages []Message
	// Schema, if set, is the JSON schema the answer must follow (structured outputs).
	Schema    map[string]any
	MaxTokens int
	// Effort overrides the provider's default effort ("low", "medium", "high").
	Effort string
}

type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type Response struct {
	Text       string
	Model      string
	StopReason string
	Usage      Usage
}

// Provider is one model API. onText receives text as it streams and may be nil.
type Provider interface {
	Name() string
	Generate(ctx context.Context, req Request, onText func(string)) (Response, error)
}

// StatusError is an HTTP error from a provider, returned before any output was
// streamed, so the call can safely be retried.
type StatusError struct {
	Provider   string
	StatusCode int
	Message    string
	// RetryAfter is how long the provider asked us to wait, if it said.
	RetryAfter time.Duration
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("%s returned HTTP %d: %s", e.Provider, e.StatusCode, e.Message)
}

// Temporary reports whether retrying may succeed: rate limits and overload.
func (e *StatusError) Temporary() bool {
	switch e.StatusCode {
	case 408, 429, 500, 502, 503, 504, 529:
		return true
	}
	return false
}

// ErrRefused means the model declined the request (stop reason "refusal").
var ErrRefused = errors.New("the model declined this request")

// ErrTruncated means the answer hit the token limit, so structured output is
// incomplete.
var ErrTruncated = errors.New("the model's answer was cut off at the token limit")

// cleanMessage shortens a provider error message and removes the key, in case a
// provider ever echoes it.
func cleanMessage(msg, apiKey string) string {
	msg = strings.TrimSpace(msg)
	if apiKey != "" {
		msg = strings.ReplaceAll(msg, apiKey, "***")
	}
	if len(msg) > 500 {
		msg = msg[:500] + "…"
	}
	return msg
}

// parseRetryAfter reads a Retry-After header: seconds or an HTTP date.
func parseRetryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if d, err := time.ParseDuration(v + "s"); err == nil && d >= 0 {
		return d
	}
	if t, err := time.Parse(time.RFC1123, v); err == nil {
		return max(time.Until(t), 0)
	}
	return 0
}

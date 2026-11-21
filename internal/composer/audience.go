// Package composer turns a marketer's brief into an audience rule and message
// copy with an LLM, and validates what comes back before anything uses it.
package composer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"campaign-composer/internal/llm"
	"campaign-composer/internal/segment"
)

type Composer struct {
	LLM   llm.Provider
	Log   *slog.Logger
	Retry llm.RetryPolicy
	// OnCall, if set, is told about every model call (for metrics).
	OnCall func(task, outcome string, usage llm.Usage)
}

func (c *Composer) report(task string, resp llm.Response, err error, valid bool) {
	outcome := "ok"
	switch {
	case err != nil:
		outcome = "error"
	case !valid:
		outcome = "invalid_output"
	}
	if c.OnCall != nil {
		c.OnCall(task, outcome, resp.Usage)
	}
}

func New(provider llm.Provider, log *slog.Logger) *Composer {
	return &Composer{LLM: provider, Log: log, Retry: llm.DefaultRetry}
}

// Audience is the LLM's answer for the audience step, after validation.
type Audience struct {
	Rule        segment.Rule `json:"rule"`
	Summary     string       `json:"summary"`     // one sentence for the marketer
	Assumptions []string     `json:"assumptions"` // interpretations the model made
	Attempts    int          `json:"attempts"`    // 2 means the repair round was needed
	Model       string       `json:"model"`
}

// ErrInvalidOutput means the model's answer still failed validation after the
// repair round. Problems lists what was wrong, for the marketer.
type ErrInvalidOutput struct {
	Problems []string
}

func (e *ErrInvalidOutput) Error() string {
	return "the model's answer was not valid: " + strings.Join(e.Problems, "; ")
}

// maxAttempts is the first answer plus one repair round with the problems.
const maxAttempts = 2

// GenerateAudience builds an audience rule from a brief.
func (c *Composer) GenerateAudience(ctx context.Context, brief string) (Audience, error) {
	return c.audience(ctx, audiencePrompt(brief, nil, ""))
}

// RefineAudience changes an existing rule according to an instruction such as
// "only English speakers" or "make it 14 days".
func (c *Composer) RefineAudience(ctx context.Context, brief string, current segment.Rule, instruction string) (Audience, error) {
	return c.audience(ctx, audiencePrompt(brief, &current, instruction))
}

func (c *Composer) audience(ctx context.Context, prompt string) (Audience, error) {
	userText := prompt
	var problems []string
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		resp, err := llm.GenerateWithRetry(ctx, c.LLM, llm.Request{
			Task:      TaskAudience,
			System:    audienceSystem(),
			Messages:  []llm.Message{{Role: llm.RoleUser, Text: userText}},
			Schema:    audienceSchema(),
			MaxTokens: 4000,
		}, nil, c.Retry)
		if err != nil {
			c.report(TaskAudience, resp, err, false)
			return Audience{}, err
		}
		c.Log.Info("llm call", "task", TaskAudience, "model", resp.Model, "attempt", attempt,
			"input_tokens", resp.Usage.InputTokens, "output_tokens", resp.Usage.OutputTokens)

		aud, err := parseAudience(resp.Text)
		c.report(TaskAudience, resp, nil, err == nil)
		if err == nil {
			aud.Attempts, aud.Model = attempt, resp.Model
			return aud, nil
		}
		problems = problemsOf(err)
		// One fresh request that shows the rejected answer and why, instead of
		// replaying it as an assistant turn.
		userText = prompt + "\n\nYour previous answer was rejected:\n<previous_answer>\n" + resp.Text +
			"\n</previous_answer>\n<problems>\n- " + strings.Join(problems, "\n- ") +
			"\n</problems>\nReturn a corrected answer. Use only the fields and values listed in the instructions."
	}
	return Audience{}, &ErrInvalidOutput{Problems: problems}
}

func parseAudience(text string) (Audience, error) {
	var raw struct {
		Rule        json.RawMessage `json:"rule"`
		Summary     string          `json:"summary"`
		Assumptions []string        `json:"assumptions"`
	}
	dec := json.NewDecoder(strings.NewReader(text))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&raw); err != nil {
		return Audience{}, fmt.Errorf("answer is not the expected JSON object: %v", err)
	}
	if len(bytes.TrimSpace(raw.Rule)) == 0 {
		return Audience{}, errors.New("answer has no rule")
	}
	rule, err := segment.Parse(raw.Rule)
	if err != nil {
		return Audience{}, err
	}
	if raw.Assumptions == nil {
		raw.Assumptions = []string{}
	}
	return Audience{Rule: rule, Summary: strings.TrimSpace(raw.Summary), Assumptions: raw.Assumptions}, nil
}

func problemsOf(err error) []string {
	var ve *segment.ValidationError
	if errors.As(err, &ve) {
		return ve.Problems
	}
	return []string{err.Error()}
}

// audienceSchema is the structured-output format: the rule schema plus a
// summary and assumptions. $defs must live at the document root.
func audienceSchema() map[string]any {
	rule := segment.Schema()
	defs := rule["$defs"]
	delete(rule, "$defs")
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []string{"rule", "summary", "assumptions"},
		"properties": map[string]any{
			"rule":        rule,
			"summary":     map[string]any{"type": "string"},
			"assumptions": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		},
		"$defs": defs,
	}
}

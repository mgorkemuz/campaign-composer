// Package llmtest provides a scripted model provider for tests. Ported from
// agents-platform.
package llmtest

import (
	"context"
	"errors"
	"sync"

	"campaign-composer/internal/llm"
)

// Step produces one model response.
type Step func(ctx context.Context, req llm.Request, onText func(string)) (llm.Response, error)

// Fake returns its steps in order, one per Generate call, and records what it
// was asked.
type Fake struct {
	mu       sync.Mutex
	steps    []Step
	requests []llm.Request
}

func New(steps ...Step) *Fake { return &Fake{steps: steps} }

func (f *Fake) Name() string { return "fake" }

// Script replaces the remaining steps.
func (f *Fake) Script(steps ...Step) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.steps = steps
}

func (f *Fake) Generate(ctx context.Context, req llm.Request, onText func(string)) (llm.Response, error) {
	f.mu.Lock()
	f.requests = append(f.requests, req)
	if len(f.steps) == 0 {
		f.mu.Unlock()
		return llm.Response{}, errors.New("fake provider: no scripted response left")
	}
	step := f.steps[0]
	f.steps = f.steps[1:]
	f.mu.Unlock()
	return step(ctx, req, onText)
}

// Requests returns copies of the requests received so far.
func (f *Fake) Requests() []llm.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]llm.Request(nil), f.requests...)
}

var usage = llm.Usage{InputTokens: 100, OutputTokens: 20}

// Text answers with text, streamed in two pieces.
func Text(s string) Step {
	return func(ctx context.Context, req llm.Request, onText func(string)) (llm.Response, error) {
		if onText != nil && len(s) > 1 {
			onText(s[:len(s)/2])
			onText(s[len(s)/2:])
		}
		return llm.Response{Text: s, Model: "fake", StopReason: "end_turn", Usage: usage}, nil
	}
}

// Fail returns err.
func Fail(err error) Step {
	return func(ctx context.Context, req llm.Request, onText func(string)) (llm.Response, error) {
		return llm.Response{}, err
	}
}

// Wait blocks until release is closed or the call is cancelled, then runs next.
func Wait(release <-chan struct{}, next Step) Step {
	return func(ctx context.Context, req llm.Request, onText func(string)) (llm.Response, error) {
		select {
		case <-release:
			return next(ctx, req, onText)
		case <-ctx.Done():
			return llm.Response{}, context.Cause(ctx)
		}
	}
}

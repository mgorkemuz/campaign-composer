package llm

import (
	"context"
	"errors"
	"fmt"
	"time"

	"campaign-composer/internal/backoff"
)

type RetryPolicy struct {
	// MaxAttempts counts the first call too.
	MaxAttempts int
	Backoff     backoff.Policy
	// MaxWait caps a single wait. If the provider asks for longer, the call
	// fails at once with a clear error instead of keeping the marketer waiting.
	MaxWait time.Duration
}

var DefaultRetry = RetryPolicy{
	MaxAttempts: 3,
	Backoff:     backoff.Policy{Base: time.Second, Max: 8 * time.Second},
	MaxWait:     20 * time.Second,
}

// GenerateWithRetry calls the provider and retries rate limits and temporary
// server errors, waiting as long as the provider asks (Retry-After) or backing
// off with jitter. Errors after output started streaming are not StatusErrors,
// so a half-streamed answer is never repeated.
func GenerateWithRetry(ctx context.Context, p Provider, req Request, onText func(string), pol RetryPolicy) (Response, error) {
	for attempt := 1; ; attempt++ {
		resp, err := p.Generate(ctx, req, onText)
		var se *StatusError
		if err == nil || !errors.As(err, &se) || !se.Temporary() {
			return resp, err
		}
		if attempt >= pol.MaxAttempts {
			return resp, fmt.Errorf("gave up after %d attempts: %w", attempt, err)
		}
		wait := se.RetryAfter
		if wait == 0 {
			wait = pol.Backoff.Delay(attempt)
		}
		if wait > pol.MaxWait {
			return resp, fmt.Errorf("%w (the provider asked to wait %s, longer than the %s limit)",
				err, wait.Round(time.Second), pol.MaxWait)
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return Response{}, context.Cause(ctx)
		case <-t.C:
		}
	}
}

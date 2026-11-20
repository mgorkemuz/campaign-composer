package llm_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"campaign-composer/internal/backoff"
	"campaign-composer/internal/llm"
	"campaign-composer/internal/llm/llmtest"
)

var fastRetry = llm.RetryPolicy{MaxAttempts: 3, Backoff: backoff.Policy{Base: time.Millisecond, Max: 5 * time.Millisecond}, MaxWait: time.Second}

func TestRetryOnRateLimitThenSucceed(t *testing.T) {
	fake := llmtest.New(
		llmtest.Fail(&llm.StatusError{Provider: "p", StatusCode: 429, RetryAfter: 10 * time.Millisecond}),
		llmtest.Fail(&llm.StatusError{Provider: "p", StatusCode: 529}),
		llmtest.Text(`{"ok":true}`),
	)
	resp, err := llm.GenerateWithRetry(context.Background(), fake, llm.Request{}, nil, fastRetry)
	if err != nil || resp.Text != `{"ok":true}` || len(fake.Requests()) != 3 {
		t.Fatalf("resp=%+v err=%v calls=%d", resp, err, len(fake.Requests()))
	}
}

func TestNoRetryOnPermanentErrors(t *testing.T) {
	for _, err := range []error{
		&llm.StatusError{Provider: "p", StatusCode: 400, Message: "bad request"},
		&llm.StatusError{Provider: "p", StatusCode: 401},
		llm.ErrRefused,
		errors.New("stream broke after output started"),
	} {
		fake := llmtest.New(llmtest.Fail(err), llmtest.Text("never"))
		if _, got := llm.GenerateWithRetry(context.Background(), fake, llm.Request{}, nil, fastRetry); !errors.Is(got, err) {
			t.Errorf("err = %v, want %v unchanged", got, err)
		}
		if n := len(fake.Requests()); n != 1 {
			t.Errorf("%v: called %d times, want 1", err, n)
		}
	}
}

func TestGiveUpAfterMaxAttempts(t *testing.T) {
	busy := &llm.StatusError{Provider: "p", StatusCode: 503}
	fake := llmtest.New(llmtest.Fail(busy), llmtest.Fail(busy), llmtest.Fail(busy), llmtest.Text("late"))
	_, err := llm.GenerateWithRetry(context.Background(), fake, llm.Request{}, nil, fastRetry)
	if !errors.Is(err, busy) || !strings.Contains(err.Error(), "gave up after 3 attempts") {
		t.Fatalf("err = %v", err)
	}
}

func TestRetryAfterLongerThanLimitFailsFast(t *testing.T) {
	fake := llmtest.New(llmtest.Fail(&llm.StatusError{Provider: "p", StatusCode: 429, RetryAfter: time.Hour}))
	start := time.Now()
	_, err := llm.GenerateWithRetry(context.Background(), fake, llm.Request{}, nil, fastRetry)
	if err == nil || !strings.Contains(err.Error(), "longer than") || time.Since(start) > time.Second {
		t.Fatalf("err = %v after %s", err, time.Since(start))
	}
}

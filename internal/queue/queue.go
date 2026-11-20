// Package queue is a PostgreSQL-backed job queue, adapted from the agents-platform
// run lease pattern.
//
//   - Claiming uses FOR UPDATE SKIP LOCKED, so many workers never take the same job.
//   - A claimed job is leased to one worker. Only the lease holder can finish it.
//   - A sweeper requeues jobs whose lease expired (their worker died).
//   - Failures are retried with exponential backoff and jitter, or after the delay
//     the handler asks for (e.g. a provider's Retry-After), until MaxAttempts.
//   - A dedup key makes enqueueing idempotent.
//
// Jobs here are short (one provider call), so the lease is not renewed with a
// heartbeat as agents-platform's long runs are: the handler's context ends
// before the lease does instead.
package queue

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"campaign-composer/internal/events"
	"campaign-composer/internal/store"
)

const (
	StatusQueued    = "queued"
	StatusRunning   = "running"
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
)

type Job struct {
	ID          int64           `json:"id"`
	Kind        string          `json:"kind"`
	DedupKey    string          `json:"dedup_key,omitempty"`
	Payload     json.RawMessage `json:"payload"`
	Attempts    int             `json:"attempts"` // including the current one
	MaxAttempts int             `json:"max_attempts"`
	LastError   string          `json:"last_error,omitempty"`
	RunAt       time.Time       `json:"run_at"`
}

type EnqueueOptions struct {
	// DedupKey, if set, makes the enqueue idempotent: a second job with the same
	// key is not created.
	DedupKey    string
	MaxAttempts int       // default 5
	RunAt       time.Time // default now; also how scheduled jobs work
}

// Topic is the event topic that wakes a kind's workers.
func Topic(kind string) string { return "queue:" + kind }

// Enqueue adds a job. Run it in the caller's transaction to make the job part of
// a state change; workers are woken when it commits. created is false when a job
// with the same dedup key already existed.
func Enqueue(ctx context.Context, db store.DBTX, kind string, payload any, opt EnqueueOptions) (id int64, created bool, err error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return 0, false, err
	}
	if opt.MaxAttempts <= 0 {
		opt.MaxAttempts = 5
	}
	var dedup *string
	if opt.DedupKey != "" {
		dedup = &opt.DedupKey
	}
	var runAt *time.Time
	if !opt.RunAt.IsZero() {
		runAt = &opt.RunAt
	}
	err = db.QueryRow(ctx, `
		INSERT INTO jobs (kind, dedup_key, payload, max_attempts, run_at)
		VALUES ($1, $2, $3, $4, coalesce($5, now()))
		ON CONFLICT (dedup_key) DO NOTHING
		RETURNING id`, kind, dedup, b, opt.MaxAttempts, runAt).Scan(&id)
	if errors.Is(store.MapErr(err), store.ErrNotFound) {
		return 0, false, nil // dedup key already taken
	}
	if err != nil {
		return 0, false, store.MapErr(err)
	}
	return id, true, events.Ring(ctx, db, Topic(kind))
}

// permanentError marks a failure that retrying cannot fix.
type permanentError struct{ err error }

func (e *permanentError) Error() string { return e.err.Error() }
func (e *permanentError) Unwrap() error { return e.err }

// Permanent wraps err so the job fails at once instead of being retried.
func Permanent(err error) error { return &permanentError{err} }

type retryAfterError struct {
	err   error
	after time.Duration
}

func (e *retryAfterError) Error() string { return e.err.Error() }
func (e *retryAfterError) Unwrap() error { return e.err }

// RetryAfter wraps err so the next attempt waits d instead of the backoff delay.
func RetryAfter(err error, d time.Duration) error { return &retryAfterError{err, d} }

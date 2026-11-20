package queue

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"campaign-composer/internal/backoff"
	"campaign-composer/internal/events"
	"campaign-composer/internal/store"
)

// Handler processes one job. Return nil on success, Permanent(err) for a failure
// retrying cannot fix, RetryAfter(err, d) to wait d before the next attempt, or
// any other error to retry with backoff.
type Handler func(ctx context.Context, job Job) error

// Worker runs a pool of goroutines that claim and process jobs of one kind.
type Worker struct {
	Pool    *pgxpool.Pool
	Hub     *events.Hub // optional: without it, workers only poll
	Log     *slog.Logger
	Kind    string
	Handler Handler
	// OnFailed is called once when a job fails for good (permanent error,
	// attempts used up, or lease expired on the last attempt).
	OnFailed func(ctx context.Context, job Job, err error)

	Concurrency int            // default 4
	LeaseTTL    time.Duration  // default 30s
	PollEvery   time.Duration  // default 1s; doorbells usually wake workers sooner
	SweepEvery  time.Duration  // default 10s
	Backoff     backoff.Policy // default 1s base, 60s cap

	owner string
}

// ErrLeaseExpired is what OnFailed receives for a job whose worker vanished.
var ErrLeaseExpired = errors.New("lease expired: the worker stopped before finishing")

func (w *Worker) setDefaults() {
	if w.Concurrency <= 0 {
		w.Concurrency = 4
	}
	if w.LeaseTTL <= 0 {
		w.LeaseTTL = 30 * time.Second
	}
	if w.PollEvery <= 0 {
		w.PollEvery = time.Second
	}
	if w.SweepEvery <= 0 {
		w.SweepEvery = 10 * time.Second
	}
	if w.Backoff == (backoff.Policy{}) {
		w.Backoff = backoff.Policy{Base: time.Second, Max: time.Minute}
	}
	if w.Log == nil {
		w.Log = slog.Default()
	}
	if w.owner == "" {
		host, _ := os.Hostname()
		b := make([]byte, 4)
		rand.Read(b)
		w.owner = fmt.Sprintf("%s/%d/%s", host, os.Getpid(), hex.EncodeToString(b))
	}
}

// handlerTimeout ends the handler before its lease, so the sweeper never
// requeues a job that is still being worked on.
func (w *Worker) handlerTimeout() time.Duration {
	return max(w.LeaseTTL*4/5, w.LeaseTTL-5*time.Second)
}

// Run processes jobs until ctx is cancelled, then waits for jobs in progress to
// finish (each bounded by the lease).
func (w *Worker) Run(ctx context.Context) {
	w.setDefaults()
	log := w.Log.With("kind", w.Kind)

	wake := make(chan struct{}, w.Concurrency)
	if w.Hub != nil {
		sub := w.Hub.Subscribe(Topic(w.Kind))
		defer w.Hub.Unsubscribe(sub)
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case <-sub.C:
					for range w.Concurrency { // wake every idle goroutine
						select {
						case wake <- struct{}{}:
						default:
						}
					}
				}
			}
		}()
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTicker(w.SweepEvery)
		defer t.Stop()
		for {
			w.Sweep(ctx)
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()

	for range w.Concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				job, ok, err := w.claim(ctx)
				if err != nil && ctx.Err() == nil {
					log.Error("claim job", "error", err)
				}
				if !ok {
					select {
					case <-ctx.Done():
					case <-wake:
					case <-time.After(w.PollEvery):
					}
					continue
				}
				w.process(job)
			}
		}()
	}
	wg.Wait()
}

func (w *Worker) claim(ctx context.Context) (Job, bool, error) {
	var j Job
	err := w.Pool.QueryRow(ctx, `
		UPDATE jobs SET status = 'running', lease_owner = $2,
			lease_expires_at = now() + make_interval(secs => $3),
			attempts = attempts + 1, updated_at = now()
		WHERE id = (
			SELECT id FROM jobs
			WHERE kind = $1 AND status = 'queued' AND run_at <= now()
			ORDER BY run_at, id LIMIT 1
			FOR UPDATE SKIP LOCKED)
		RETURNING id, kind, coalesce(dedup_key, ''), payload, attempts, max_attempts, last_error, run_at`,
		w.Kind, w.owner, w.LeaseTTL.Seconds(),
	).Scan(&j.ID, &j.Kind, &j.DedupKey, &j.Payload, &j.Attempts, &j.MaxAttempts, &j.LastError, &j.RunAt)
	if errors.Is(store.MapErr(err), store.ErrNotFound) {
		return Job{}, false, nil
	}
	if err != nil {
		return Job{}, false, err
	}
	return j, true, nil
}

// process runs the handler and records the outcome. It deliberately does not use
// the Run context: on shutdown, a job in progress finishes instead of being cut
// off half way.
func (w *Worker) process(job Job) {
	log := w.Log.With("kind", job.Kind, "job", job.ID, "attempt", job.Attempts)
	ctx, cancel := context.WithTimeout(context.Background(), w.handlerTimeout())
	err := w.safeHandle(ctx, job)
	cancel()

	db, done := context.WithTimeout(context.Background(), 5*time.Second)
	defer done()

	if err == nil {
		if !w.finish(db, job.ID, StatusSucceeded, "") {
			log.Warn("job finished after its lease was lost")
		}
		return
	}

	var perm *permanentError
	if errors.As(err, &perm) || job.Attempts >= job.MaxAttempts {
		if w.finish(db, job.ID, StatusFailed, err.Error()) && w.OnFailed != nil {
			job.LastError = err.Error()
			w.OnFailed(db, job, err)
		}
		log.Warn("job failed", "error", err)
		return
	}

	delay := w.Backoff.Delay(job.Attempts)
	var ra *retryAfterError
	if errors.As(err, &ra) {
		delay = ra.after
	}
	tag, uerr := w.Pool.Exec(db, `
		UPDATE jobs SET status = 'queued', run_at = now() + make_interval(secs => $3),
			last_error = $4, lease_owner = NULL, lease_expires_at = NULL, updated_at = now()
		WHERE id = $1 AND lease_owner = $2 AND status = 'running'`,
		job.ID, w.owner, delay.Seconds(), err.Error())
	if uerr != nil {
		log.Error("requeue job", "error", uerr)
	} else if tag.RowsAffected() == 0 {
		log.Warn("job lease was lost before it could be requeued")
	}
	log.Info("job will be retried", "error", err, "delay", delay)
}

// safeHandle turns a panic in the handler into an error, so one bad job cannot
// kill the worker goroutine.
func (w *Worker) safeHandle(ctx context.Context, job Job) (err error) {
	defer func() {
		if v := recover(); v != nil {
			err = fmt.Errorf("handler panicked: %v", v)
		}
	}()
	return w.Handler(ctx, job)
}

// finish moves a job this worker holds to a final status. It reports false if
// the lease was lost (the sweeper or another worker owns the job now).
func (w *Worker) finish(ctx context.Context, id int64, status, errMsg string) bool {
	tag, err := w.Pool.Exec(ctx, `
		UPDATE jobs SET status = $3, last_error = $4, finished_at = now(), updated_at = now(),
			lease_owner = NULL, lease_expires_at = NULL
		WHERE id = $1 AND lease_owner = $2 AND status = 'running'`, id, w.owner, status, errMsg)
	if err != nil {
		w.Log.Error("finish job", "job", id, "error", err)
		return false
	}
	return tag.RowsAffected() == 1
}

// Sweep requeues this kind's jobs whose lease expired, or fails them if that was
// their last attempt. Every process may run it; SKIP LOCKED keeps runs apart.
func (w *Worker) Sweep(ctx context.Context) {
	w.setDefaults()
	rows, err := w.Pool.Query(ctx, `
		WITH expired AS (
			SELECT id FROM jobs
			WHERE kind = $1 AND status = 'running' AND lease_expires_at < now()
			FOR UPDATE SKIP LOCKED)
		UPDATE jobs j SET
			status      = CASE WHEN j.attempts >= j.max_attempts THEN 'failed' ELSE 'queued' END,
			finished_at = CASE WHEN j.attempts >= j.max_attempts THEN now() END,
			run_at = now(), last_error = $2, lease_owner = NULL, lease_expires_at = NULL, updated_at = now()
		FROM expired WHERE j.id = expired.id
		RETURNING j.id, j.kind, coalesce(j.dedup_key, ''), j.payload, j.attempts, j.max_attempts, j.status`,
		w.Kind, ErrLeaseExpired.Error())
	if err != nil {
		if ctx.Err() == nil {
			w.Log.Error("sweep jobs", "kind", w.Kind, "error", err)
		}
		return
	}
	var failed []Job
	requeued := 0
	for rows.Next() {
		var j Job
		var status string
		if err := rows.Scan(&j.ID, &j.Kind, &j.DedupKey, &j.Payload, &j.Attempts, &j.MaxAttempts, &status); err != nil {
			w.Log.Error("sweep jobs: scan", "error", err)
			continue
		}
		j.LastError = ErrLeaseExpired.Error()
		if status == StatusFailed {
			failed = append(failed, j)
		} else {
			requeued++
		}
	}
	rows.Close()

	if requeued > 0 {
		w.Log.Warn("requeued jobs whose worker stopped", "kind", w.Kind, "count", requeued)
		events.Ring(ctx, w.Pool, Topic(w.Kind))
	}
	for _, j := range failed {
		if w.OnFailed != nil {
			w.OnFailed(ctx, j, ErrLeaseExpired)
		}
	}
}

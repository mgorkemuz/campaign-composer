package queue_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"campaign-composer/internal/backoff"
	"campaign-composer/internal/events"
	"campaign-composer/internal/queue"
	"campaign-composer/internal/testdb"
)

func TestMain(m *testing.M) { os.Exit(testdb.Main(m)) }

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// fast keeps retries quick in tests.
var fast = backoff.Policy{Base: 5 * time.Millisecond, Max: 20 * time.Millisecond}

func startWorker(t *testing.T, w *queue.Worker) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type jobRow struct {
	Status    string
	Attempts  int
	LastError string
}

func row(t *testing.T, pool *pgxpool.Pool, id int64) jobRow {
	t.Helper()
	var r jobRow
	if err := pool.QueryRow(context.Background(),
		`SELECT status, attempts, last_error FROM jobs WHERE id = $1`, id).Scan(&r.Status, &r.Attempts, &r.LastError); err != nil {
		t.Fatal(err)
	}
	return r
}

func countStatus(t *testing.T, pool *pgxpool.Pool, kind, status string) int {
	t.Helper()
	var n int
	pool.QueryRow(context.Background(), `SELECT count(*) FROM jobs WHERE kind = $1 AND status = $2`, kind, status).Scan(&n)
	return n
}

func TestEnqueueDedup(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()

	id1, created1, err := queue.Enqueue(ctx, pool, "send", map[string]int{"n": 1}, queue.EnqueueOptions{DedupKey: "delivery:42"})
	if err != nil || !created1 {
		t.Fatalf("first enqueue: id=%d created=%v err=%v", id1, created1, err)
	}
	_, created2, err := queue.Enqueue(ctx, pool, "send", map[string]int{"n": 2}, queue.EnqueueOptions{DedupKey: "delivery:42"})
	if err != nil || created2 {
		t.Fatalf("second enqueue: created=%v err=%v; want a silent no-op", created2, err)
	}
	// Jobs without a key are never deduplicated.
	for range 2 {
		if _, created, err := queue.Enqueue(ctx, pool, "send", nil, queue.EnqueueOptions{}); err != nil || !created {
			t.Fatalf("keyless enqueue: created=%v err=%v", created, err)
		}
	}
	if n := countStatus(t, pool, "send", queue.StatusQueued); n != 3 {
		t.Fatalf("queued jobs = %d, want 3", n)
	}
}

func TestEachJobRunsExactlyOnceAcrossWorkers(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	const jobs = 200

	var mu sync.Mutex
	seen := map[string]int{}
	handler := func(ctx context.Context, j queue.Job) error {
		mu.Lock()
		seen[string(j.Payload)]++
		mu.Unlock()
		return nil
	}
	// Two independent workers (as if two processes), eight goroutines each.
	for range 2 {
		startWorker(t, &queue.Worker{Pool: pool, Log: quiet, Kind: "send", Handler: handler,
			Concurrency: 8, PollEvery: 10 * time.Millisecond})
	}
	for i := range jobs {
		if _, _, err := queue.Enqueue(ctx, pool, "send", strconv.Itoa(i), queue.EnqueueOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, "all jobs to succeed", func() bool { return countStatus(t, pool, "send", queue.StatusSucceeded) == jobs })

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != jobs {
		t.Fatalf("handled %d distinct jobs, want %d", len(seen), jobs)
	}
	for k, n := range seen {
		if n != 1 {
			t.Fatalf("job %s ran %d times", k, n)
		}
	}
}

func TestRetriesThenSucceeds(t *testing.T) {
	pool := testdb.New(t)
	var calls atomic.Int32
	startWorker(t, &queue.Worker{Pool: pool, Log: quiet, Kind: "flaky", Backoff: fast, PollEvery: 5 * time.Millisecond,
		Handler: func(ctx context.Context, j queue.Job) error {
			if calls.Add(1) < 3 {
				return errors.New("provider returned 503")
			}
			return nil
		}})
	id, _, _ := queue.Enqueue(context.Background(), pool, "flaky", nil, queue.EnqueueOptions{})

	waitFor(t, "success", func() bool { return row(t, pool, id).Status == queue.StatusSucceeded })
	if r := row(t, pool, id); r.Attempts != 3 || r.LastError != "" {
		t.Fatalf("row = %+v, want 3 attempts and the error cleared", r)
	}
}

func TestRetryAfterIsRespected(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	var calls atomic.Int32
	startWorker(t, &queue.Worker{Pool: pool, Log: quiet, Kind: "limited", Backoff: fast, PollEvery: 5 * time.Millisecond,
		Handler: func(ctx context.Context, j queue.Job) error {
			calls.Add(1)
			return queue.RetryAfter(errors.New("429 too many requests"), 3*time.Second)
		}})
	id, _, _ := queue.Enqueue(ctx, pool, "limited", nil, queue.EnqueueOptions{})

	waitFor(t, "first attempt", func() bool { return calls.Load() == 1 && row(t, pool, id).Status == queue.StatusQueued })
	var wait float64
	pool.QueryRow(ctx, `SELECT extract(epoch FROM run_at - now()) FROM jobs WHERE id = $1`, id).Scan(&wait)
	if wait < 2 || wait > 3.1 {
		t.Fatalf("next attempt in %.2fs, want about 3s (Retry-After), not the %s backoff", wait, fast.Max)
	}
	time.Sleep(200 * time.Millisecond)
	if n := calls.Load(); n != 1 {
		t.Fatalf("handler called %d times before Retry-After passed", n)
	}
}

func TestPermanentErrorAndExhaustedAttemptsFail(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()

	var mu sync.Mutex
	failed := map[int64]string{}
	startWorker(t, &queue.Worker{Pool: pool, Log: quiet, Kind: "bad", Backoff: fast, PollEvery: 5 * time.Millisecond,
		Handler: func(ctx context.Context, j queue.Job) error {
			if string(j.Payload) == `"invalid"` {
				return queue.Permanent(errors.New("400 invalid phone number"))
			}
			return errors.New("503 unavailable")
		},
		OnFailed: func(ctx context.Context, j queue.Job, err error) {
			mu.Lock()
			defer mu.Unlock()
			if _, dup := failed[j.ID]; dup {
				t.Errorf("OnFailed called twice for job %d", j.ID)
			}
			failed[j.ID] = err.Error()
		}})

	permID, _, _ := queue.Enqueue(ctx, pool, "bad", "invalid", queue.EnqueueOptions{})
	retryID, _, _ := queue.Enqueue(ctx, pool, "bad", "down", queue.EnqueueOptions{MaxAttempts: 3})

	waitFor(t, "both to fail", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(failed) == 2
	})
	if r := row(t, pool, permID); r.Status != queue.StatusFailed || r.Attempts != 1 {
		t.Errorf("permanent: %+v, want failed after 1 attempt", r)
	}
	if r := row(t, pool, retryID); r.Status != queue.StatusFailed || r.Attempts != 3 || r.LastError != "503 unavailable" {
		t.Errorf("exhausted: %+v, want failed after 3 attempts", r)
	}
}

func TestPanicIsRetriedNotFatal(t *testing.T) {
	pool := testdb.New(t)
	var calls atomic.Int32
	startWorker(t, &queue.Worker{Pool: pool, Log: quiet, Kind: "panicky", Backoff: fast, PollEvery: 5 * time.Millisecond,
		Handler: func(ctx context.Context, j queue.Job) error {
			if calls.Add(1) == 1 {
				panic("boom")
			}
			return nil
		}})
	id, _, _ := queue.Enqueue(context.Background(), pool, "panicky", nil, queue.EnqueueOptions{})
	waitFor(t, "success after a panic", func() bool { return row(t, pool, id).Status == queue.StatusSucceeded })
}

func TestSweepRequeuesExpiredLeases(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()

	// Simulate two jobs whose worker died mid-way: one with attempts left, one on its last.
	var alive, last int64
	pool.QueryRow(ctx, `INSERT INTO jobs (kind, status, attempts, max_attempts, lease_owner, lease_expires_at)
		VALUES ('crash', 'running', 1, 5, 'dead-worker', now() - interval '1 second') RETURNING id`).Scan(&alive)
	pool.QueryRow(ctx, `INSERT INTO jobs (kind, status, attempts, max_attempts, lease_owner, lease_expires_at)
		VALUES ('crash', 'running', 5, 5, 'dead-worker', now() - interval '1 second') RETURNING id`).Scan(&last)
	// A live lease must not be touched.
	var live int64
	pool.QueryRow(ctx, `INSERT INTO jobs (kind, status, attempts, lease_owner, lease_expires_at)
		VALUES ('crash', 'running', 1, 'busy-worker', now() + interval '1 minute') RETURNING id`).Scan(&live)

	var failed []int64
	w := &queue.Worker{Pool: pool, Log: quiet, Kind: "crash",
		OnFailed: func(ctx context.Context, j queue.Job, err error) {
			if !errors.Is(err, queue.ErrLeaseExpired) {
				t.Errorf("OnFailed err = %v", err)
			}
			failed = append(failed, j.ID)
		}}
	w.Sweep(ctx)

	if r := row(t, pool, alive); r.Status != queue.StatusQueued {
		t.Errorf("job with attempts left: %+v, want queued", r)
	}
	if r := row(t, pool, last); r.Status != queue.StatusFailed {
		t.Errorf("job on its last attempt: %+v, want failed", r)
	}
	if r := row(t, pool, live); r.Status != queue.StatusRunning {
		t.Errorf("live lease: %+v, want untouched", r)
	}
	if len(failed) != 1 || failed[0] != last {
		t.Errorf("OnFailed for %v, want [%d]", failed, last)
	}
}

func TestDoorbellWakesIdleWorkers(t *testing.T) {
	pool := testdb.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hub := events.NewHub(pool, quiet)
	go hub.Run(ctx)
	<-hub.Ready()

	var done atomic.Bool
	startWorker(t, &queue.Worker{Pool: pool, Hub: hub, Log: quiet, Kind: "wake",
		PollEvery: time.Minute, // polling alone would take a minute
		Handler:   func(ctx context.Context, j queue.Job) error { done.Store(true); return nil }})
	time.Sleep(100 * time.Millisecond) // let the workers go idle

	start := time.Now()
	queue.Enqueue(ctx, pool, "wake", nil, queue.EnqueueOptions{})
	waitFor(t, "the doorbell to wake a worker", done.Load)
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("took %s; the doorbell did not wake the worker", d)
	}
}

func TestScheduledJobWaitsForRunAt(t *testing.T) {
	pool := testdb.New(t)
	var ran atomic.Bool
	startWorker(t, &queue.Worker{Pool: pool, Log: quiet, Kind: "later", PollEvery: 10 * time.Millisecond,
		Handler: func(ctx context.Context, j queue.Job) error { ran.Store(true); return nil }})
	queue.Enqueue(context.Background(), pool, "later", nil, queue.EnqueueOptions{RunAt: time.Now().Add(time.Hour)})
	time.Sleep(200 * time.Millisecond)
	if ran.Load() {
		t.Fatal("a job scheduled an hour ahead ran now")
	}
}

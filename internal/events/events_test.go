package events_test

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"campaign-composer/internal/events"
	"campaign-composer/internal/testdb"
)

func TestMain(m *testing.M) { os.Exit(testdb.Main(m)) }

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestAddGivesGapFreeSeqUnderConcurrency(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()

	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := events.Add(ctx, pool, "campaign:1", "tick", map[string]int{"i": i}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	events.Add(ctx, pool, "campaign:2", "other", nil)

	got, err := events.Since(ctx, pool, "campaign:1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 20 {
		t.Fatalf("got %d events, want 20", len(got))
	}
	for i, e := range got {
		if e.Seq != int64(i+1) {
			t.Fatalf("event %d has seq %d; want 1..20 in order", i, e.Seq)
		}
	}
	tail, _ := events.Since(ctx, pool, "campaign:1", 18)
	if len(tail) != 2 || tail[0].Seq != 19 {
		t.Fatalf("Since(18) = %+v", tail)
	}
}

func TestHubDeliversDoorbellsAndDeltas(t *testing.T) {
	pool := testdb.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	hub := events.NewHub(pool, quiet)
	go hub.Run(ctx)
	<-hub.Ready()

	sub := hub.Subscribe("campaign:7")
	defer hub.Unsubscribe(sub)
	other := hub.Subscribe("campaign:8")
	defer hub.Unsubscribe(other)

	long := strings.Repeat("ş", 1500) // 3000 bytes: split into two notifications
	if err := events.Delta(ctx, pool, "campaign:7", long); err != nil {
		t.Fatal(err)
	}
	if _, err := events.Add(ctx, pool, "campaign:7", "status", map[string]string{"status": "sending"}); err != nil {
		t.Fatal(err)
	}

	var text strings.Builder
	rang := false
	deadline := time.After(5 * time.Second)
	for !rang || text.Len() < len(long) {
		select {
		case sig := <-sub.C:
			if sig.Delta == "" {
				rang = true
			}
			text.WriteString(sig.Delta)
		case <-deadline:
			t.Fatalf("timed out: rang=%v, got %d of %d delta bytes", rang, text.Len(), len(long))
		}
	}
	if text.String() != long {
		t.Fatal("deltas did not reassemble into the original text")
	}
	select {
	case sig := <-other.C:
		// The hub rings every topic once when it starts listening; anything else is a leak.
		if sig.Delta != "" {
			t.Fatalf("other topic received a delta: %q", sig.Delta)
		}
	default:
	}
}

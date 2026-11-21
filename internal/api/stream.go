package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"campaign-composer/internal/events"
)

// keepAliveEvery also bounds how late a missed doorbell can delay an event.
var keepAliveEvery = 15 * time.Second

type streamOptions struct {
	// Finished is checked after each read; when it reports true the stream
	// sends "end" so the browser does not reconnect.
	Finished func(ctx context.Context) (bool, error)
	// Snapshot, if set, is sent as a "progress" event when the stream opens and
	// after doorbells, at most once per SnapshotEvery (with a trailing send, so
	// the latest state always arrives).
	Snapshot      func(ctx context.Context) (any, error)
	SnapshotEvery time.Duration
}

// streamTopic streams a topic's events with server-sent events. Ported from the
// agents-platform run stream.
//
// It first replays stored events after Last-Event-ID (sent by the browser when
// it reconnects) or ?after=, then waits for new ones. Stored and live events
// come from the same query, so nothing falls in between. Streamed text arrives
// as "delta" events, which are not stored: a JSON object as the producer wrote
// it (e.g. {"field":"sms","text":"..."}), or {"text":...} for plain text.
func (s *Server) streamTopic(w http.ResponseWriter, r *http.Request, topic string, opts streamOptions) {
	ctx := r.Context()
	after, _ := strconv.ParseInt(r.Header.Get("Last-Event-ID"), 10, 64)
	if v := r.URL.Query().Get("after"); v != "" {
		after, _ = strconv.ParseInt(v, 10, 64)
	}

	// Subscribe before the first query, so an event stored in between still
	// rings the bell.
	sub := s.hub.Subscribe(topic)
	defer s.hub.Unsubscribe(sub)

	rc := http.NewResponseController(w)
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no") // tell proxies not to buffer
	w.WriteHeader(http.StatusOK)
	io.WriteString(w, "retry: 2000\n\n")
	rc.Flush()

	var (
		lastSnap  time.Time
		snapTimer <-chan time.Time
	)
	sendSnapshot := func() bool {
		snapTimer = nil
		lastSnap = time.Now()
		v, err := opts.Snapshot(ctx)
		if err != nil {
			return false
		}
		data, _ := json.Marshal(v)
		fmt.Fprintf(w, "event: progress\ndata: %s\n\n", data)
		return true
	}
	maybeSnapshot := func() {
		if opts.Snapshot == nil {
			return
		}
		every := opts.SnapshotEvery
		if wait := every - time.Since(lastSnap); wait <= 0 {
			sendSnapshot()
		} else if snapTimer == nil {
			snapTimer = time.After(wait)
		}
	}

	keepAlive := time.NewTicker(keepAliveEvery)
	defer keepAlive.Stop()
	for {
		evs, err := events.Since(ctx, s.pool, topic, after)
		if err != nil {
			return
		}
		for _, ev := range evs {
			data, _ := json.Marshal(ev)
			fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", ev.Seq, ev.Type, data)
			after = ev.Seq
		}
		maybeSnapshot()
		if opts.Finished != nil {
			if done, err := opts.Finished(ctx); err != nil || done {
				if opts.Snapshot != nil {
					sendSnapshot() // the final state, even if one was sent a moment ago
				}
				io.WriteString(w, "event: end\ndata: {}\n\n")
				rc.Flush()
				return
			}
		}
		rc.Flush()

	wait:
		for {
			select {
			case <-ctx.Done():
				return
			case <-s.closing:
				return
			case <-keepAlive.C:
				io.WriteString(w, ": keep-alive\n\n")
				rc.Flush()
				break wait // also re-reads, in case a doorbell was missed
			case <-snapTimer:
				sendSnapshot()
				rc.Flush()
			case sig := <-sub.C:
				if sig.Delta == "" {
					break wait
				}
				data := []byte(sig.Delta)
				if !json.Valid(data) || data[0] != '{' {
					data, _ = json.Marshal(map[string]string{"text": sig.Delta})
				}
				fmt.Fprintf(w, "event: delta\ndata: %s\n\n", data)
				rc.Flush()
			}
		}
	}
}

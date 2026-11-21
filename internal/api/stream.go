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

// streamTopic streams a topic's events with server-sent events. Ported from the
// agents-platform run stream.
//
// It first replays stored events after Last-Event-ID (sent by the browser when
// it reconnects) or ?after=, then waits for new ones. Stored and live events come
// from the same query, so nothing falls in between. Streamed text arrives as
// "delta" events, which are not stored: a JSON object as the producer wrote it
// (e.g. {"field":"sms","text":"..."}), or {"text":...} for plain text. When finished reports true after a
// read, the stream sends "end" so the browser does not reconnect.
func (s *Server) streamTopic(w http.ResponseWriter, r *http.Request, topic string,
	finished func(ctx context.Context) (bool, error)) {
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
		rc.Flush()
		if finished != nil {
			if done, err := finished(ctx); err != nil || done {
				io.WriteString(w, "event: end\ndata: {}\n\n")
				rc.Flush()
				return
			}
		}

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

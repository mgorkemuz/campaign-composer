package events

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"campaign-composer/internal/store"
)

// Event is one stored entry in a topic's ordered stream.
type Event struct {
	Seq       int64           `json:"seq"`
	Type      string          `json:"type"`
	Data      json.RawMessage `json:"data"`
	CreatedAt time.Time       `json:"created_at"`
}

// Add stores an event and rings the topic's doorbell. Run it inside the
// caller's transaction to make the event part of a state change; the NOTIFY is
// then delivered on commit. Taking the next seq locks the topic row, so
// concurrent writers get distinct, gap-free numbers.
func Add(ctx context.Context, db store.DBTX, topic, typ string, data any) (int64, error) {
	b, err := json.Marshal(data)
	if err != nil {
		return 0, err
	}
	var seq int64
	err = db.QueryRow(ctx, `
		WITH s AS (
			INSERT INTO event_topics (topic, last_seq) VALUES ($1, 1)
			ON CONFLICT (topic) DO UPDATE SET last_seq = event_topics.last_seq + 1
			RETURNING last_seq)
		INSERT INTO events (topic, seq, type, data) SELECT $1, last_seq, $2, $3 FROM s
		RETURNING seq`, topic, typ, b).Scan(&seq)
	if err != nil {
		return 0, store.MapErr(err)
	}
	return seq, Ring(ctx, db, topic)
}

// Since returns a topic's events with seq greater than after, oldest first.
func Since(ctx context.Context, db store.DBTX, topic string, after int64) ([]Event, error) {
	rows, err := db.Query(ctx, `
		SELECT seq, type, data, created_at FROM events
		WHERE topic = $1 AND seq > $2 ORDER BY seq`, topic, after)
	if err != nil {
		return nil, store.MapErr(err)
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.Seq, &e.Type, &e.Data, &e.CreatedAt); err != nil {
			return nil, store.MapErr(err)
		}
		out = append(out, e)
	}
	return out, store.MapErr(rows.Err())
}

// Ring wakes a topic's listeners without storing anything, e.g. when delivery
// counters changed and streams should re-read them.
func Ring(ctx context.Context, db store.DBTX, topic string) error {
	return notify(ctx, db, topic, "")
}

// maxDelta keeps each NOTIFY payload well under PostgreSQL's 8000-byte limit.
const maxDelta = 2000

// Delta sends streamed text (e.g. model output) to listeners without storing
// it; the complete text is stored as an event when the stream ends. Long text
// is split on UTF-8 boundaries.
func Delta(ctx context.Context, db store.DBTX, topic, text string) error {
	for len(text) > 0 {
		n := min(len(text), maxDelta)
		for n < len(text) && !utf8.RuneStart(text[n]) {
			n--
		}
		if err := notify(ctx, db, topic, text[:n]); err != nil {
			return err
		}
		text = text[n:]
	}
	return nil
}

func notify(ctx context.Context, db store.DBTX, topic, delta string) error {
	payload, _ := json.Marshal(map[string]string{"topic": topic, "delta": delta})
	_, err := db.Exec(ctx, `SELECT pg_notify($1, $2)`, store.NotifyChannel, string(payload))
	return store.MapErr(err)
}

// DeltaBuffer batches streamed text into fewer notifications: it flushes every
// 100 ms or 1 KB. Ported from agents-platform's runner.
type DeltaBuffer struct {
	db    store.DBTX
	topic string

	mu   sync.Mutex
	buf  strings.Builder
	last time.Time
}

func NewDeltaBuffer(db store.DBTX, topic string) *DeltaBuffer {
	return &DeltaBuffer{db: db, topic: topic, last: time.Now()}
}

func (d *DeltaBuffer) Add(s string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.buf.WriteString(s)
	if d.buf.Len() >= 1000 || time.Since(d.last) > 100*time.Millisecond {
		d.flushLocked()
	}
}

// Flush sends whatever is buffered. Call it when the stream ends.
func (d *DeltaBuffer) Flush() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.flushLocked()
}

func (d *DeltaBuffer) flushLocked() {
	text := d.buf.String()
	d.buf.Reset()
	d.last = time.Now()
	if text == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = Delta(ctx, d.db, d.topic, text) // best effort: the final text is stored anyway
}

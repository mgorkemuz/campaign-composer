// Package events is the live-update layer, ported from agents-platform and
// generalised from runs to topics (e.g. one campaign).
//
// Every event is stored in PostgreSQL first; a NOTIFY on commit is only a
// doorbell. Each server process keeps one LISTEN connection and wakes its local
// subscribers, which then read new events from the database. A missed
// notification only delays an event; it never loses one.
package events

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"campaign-composer/internal/store"
)

// Signal is either a doorbell (Delta empty: re-read the stored state) or a piece
// of streamed text, which is not stored.
type Signal struct {
	Delta string
}

type Subscription struct {
	topic string
	C     <-chan Signal
	c     chan Signal
}

type Hub struct {
	pool *pgxpool.Pool
	log  *slog.Logger

	mu    sync.Mutex
	subs  map[string]map[*Subscription]struct{}
	ready chan struct{}
	once  sync.Once
}

func NewHub(pool *pgxpool.Pool, log *slog.Logger) *Hub {
	return &Hub{pool: pool, log: log, subs: map[string]map[*Subscription]struct{}{}, ready: make(chan struct{})}
}

// Ready is closed once the hub is listening.
func (h *Hub) Ready() <-chan struct{} { return h.ready }

// Subscribe starts delivering signals for one topic. Call Unsubscribe when done.
func (h *Hub) Subscribe(topic string) *Subscription {
	c := make(chan Signal, 256)
	s := &Subscription{topic: topic, C: c, c: c}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.subs[topic] == nil {
		h.subs[topic] = map[*Subscription]struct{}{}
	}
	h.subs[topic][s] = struct{}{}
	return s
}

func (h *Hub) Unsubscribe(s *Subscription) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.subs[s.topic], s)
	if len(h.subs[s.topic]) == 0 {
		delete(h.subs, s.topic)
	}
}

// send never blocks: a reader that falls behind loses streamed text, and
// catches up on stored events at its next doorbell or keep-alive.
func (h *Hub) send(topic string, sig Signal) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.subs[topic] {
		select {
		case s.c <- sig:
		default:
		}
	}
}

func (h *Hub) ringAll() {
	h.mu.Lock()
	topics := make([]string, 0, len(h.subs))
	for t := range h.subs {
		topics = append(topics, t)
	}
	h.mu.Unlock()
	for _, t := range topics {
		h.send(t, Signal{})
	}
}

// Run listens until ctx is cancelled, reconnecting after errors.
func (h *Hub) Run(ctx context.Context) {
	for {
		err := h.listen(ctx)
		if ctx.Err() != nil {
			return
		}
		h.log.Warn("event listener lost its connection; reconnecting", "error", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}

func (h *Hub) listen(ctx context.Context) error {
	pc, err := h.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	// The connection stays in LISTEN mode, so take it out of the pool.
	conn := pc.Hijack()
	defer conn.Close(context.WithoutCancel(ctx))

	if _, err := conn.Exec(ctx, "LISTEN "+store.NotifyChannel); err != nil {
		return err
	}
	h.once.Do(func() { close(h.ready) })
	// Events committed while we were not listening are read on this ring.
	h.ringAll()

	for {
		n, err := conn.WaitForNotification(ctx)
		if err != nil {
			return err
		}
		var p struct {
			Topic string `json:"topic"`
			Delta string `json:"delta"`
		}
		if json.Unmarshal([]byte(n.Payload), &p) != nil || p.Topic == "" {
			continue
		}
		h.send(p.Topic, Signal{Delta: p.Delta})
	}
}

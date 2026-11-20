// Package api is the HTTP layer: routing, middleware and JSON handlers.
package api

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"campaign-composer/internal/campaign"
	"campaign-composer/internal/events"
)

type Server struct {
	pool *pgxpool.Pool
	log  *slog.Logger
	hub  *events.Hub
	now  func() time.Time

	campaigns *campaign.Service

	closing   chan struct{}
	closeOnce sync.Once
}

type Options struct {
	Hub       *events.Hub
	Campaigns *campaign.Service
	// Now is the clock for "within N days" rules; tests pin it. Default time.Now.
	Now func() time.Time
}

func New(pool *pgxpool.Pool, log *slog.Logger, opts Options) *Server {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Server{pool: pool, log: log, hub: opts.Hub, now: opts.Now, campaigns: opts.Campaigns,
		closing: make(chan struct{})}
}

// CloseStreams ends open event streams. http.Server.Shutdown waits for active
// requests, and a stream would otherwise keep it waiting.
func (s *Server) CloseStreams() {
	s.closeOnce.Do(func() { close(s.closing) })
}

// Handler returns the full HTTP handler with all routes and middleware.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /readyz", s.handleReady)

	mux.HandleFunc("GET /api/segments/fields", s.handleSegmentFields)
	mux.HandleFunc("POST /api/segments/preview", s.handleSegmentPreview)

	mux.HandleFunc("POST /api/campaigns", s.handleCreateCampaign)
	mux.HandleFunc("GET /api/campaigns", s.handleListCampaigns)
	mux.HandleFunc("GET /api/campaigns/{id}", s.handleGetCampaign)
	mux.HandleFunc("POST /api/campaigns/{id}/audience", s.handleGenerateAudience)
	mux.HandleFunc("POST /api/campaigns/{id}/audience/refine", s.handleRefineAudience)
	mux.HandleFunc("PUT /api/campaigns/{id}/audience", s.handleSetRule)

	// Unknown /api paths get a JSON 404.
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "no such endpoint")
	})

	return s.recoverPanics(s.logRequests(mux))
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.pool.Ping(ctx); err != nil {
		writeError(w, http.StatusServiceUnavailable, "not_ready", "database unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

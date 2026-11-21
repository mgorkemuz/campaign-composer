package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"campaign-composer/internal/campaign"
	"campaign-composer/internal/delivery"
	"campaign-composer/internal/webhook"
)

func (s *Server) handleApprove(w http.ResponseWriter, r *http.Request) {
	res, err := s.campaigns.Approve(r.Context(), r.PathValue("id"))
	var ce *campaign.ContentError
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, res)
	case errors.As(err, &ce):
		writeProblems(w, http.StatusUnprocessableEntity, "invalid_content",
			"the messages no longer fit this audience; edit or regenerate them", ce.Problems)
	case errors.Is(err, campaign.ErrNotReady):
		writeError(w, http.StatusConflict, "not_ready", err.Error())
	case errors.Is(err, campaign.ErrWrongStatus):
		writeError(w, http.StatusConflict, "already_approved", "this campaign has already been approved")
	default:
		s.storeError(w, r, err, "campaign")
	}
}

func (s *Server) handleDeliveryCounts(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.campaigns.Get(r.Context(), id); err != nil {
		s.storeError(w, r, err, "campaign")
		return
	}
	counts, err := s.campaigns.DeliveryCounts(r.Context(), id)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"counts": counts})
}

func (s *Server) handleListDeliveries(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.campaigns.Get(r.Context(), id); err != nil {
		s.storeError(w, r, err, "campaign")
		return
	}
	limit, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.campaigns.Deliveries(r.Context(), id, r.URL.Query().Get("status"), limit)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deliveries": rows})
}

// handleReceipt is the provider's webhook. The signature and timestamp are
// checked before the body is trusted. Unknown or duplicate receipts get 200, so
// the provider does not keep retrying them; database errors get 500, so it does.
func (s *Server) handleReceipt(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "body too large")
		return
	}
	if err := webhook.Verify(s.webhookSecret, r.Header.Get(webhook.SignatureHeader),
		// Freshness is checked against real time: s.now is the business clock
		// that tests pin.
		r.Header.Get(webhook.TimestampHeader), body, time.Now()); err != nil {
		s.observeWebhook("rejected")
		writeError(w, http.StatusUnauthorized, "invalid_signature", err.Error())
		return
	}
	var rc delivery.Receipt
	if err := json.Unmarshal(body, &rc); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON")
		return
	}
	applied, err := delivery.ApplyReceipt(r.Context(), s.pool, rc)
	switch {
	case errors.Is(err, delivery.ErrBadReceipt):
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	case err != nil:
		s.internalError(w, r, err)
		return
	}
	outcome := "ignored"
	if applied {
		outcome = "applied"
		var campaignID string
		if s.pool.QueryRow(r.Context(), `SELECT campaign_id::text FROM deliveries WHERE id = $1::bigint`, rc.Reference).Scan(&campaignID) == nil {
			s.ring(campaign.Topic(campaignID))
		}
	}
	s.observeWebhook(outcome)
	writeJSON(w, http.StatusOK, map[string]bool{"applied": applied})
}

func (s *Server) observeWebhook(outcome string) {
	if s.metrics != nil {
		s.metrics.Webhook(outcome)
	}
}

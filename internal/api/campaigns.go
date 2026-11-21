package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"campaign-composer/internal/campaign"
	"campaign-composer/internal/composer"
	"campaign-composer/internal/llm"
	"campaign-composer/internal/message"
	"campaign-composer/internal/segment"
)

func (s *Server) handleCreateCampaign(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Brief string `json:"brief"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	c, err := s.campaigns.Create(r.Context(), req.Brief)
	if errors.Is(err, campaign.ErrInvalidBrief) {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

func (s *Server) handleListCampaigns(w http.ResponseWriter, r *http.Request) {
	list, err := s.campaigns.List(r.Context(), 50)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"campaigns": list})
}

func (s *Server) handleGetCampaign(w http.ResponseWriter, r *http.Request) {
	c, err := s.campaigns.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		s.storeError(w, r, err, "campaign")
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) handleGenerateAudience(w http.ResponseWriter, r *http.Request) {
	res, err := s.campaigns.GenerateAudience(r.Context(), r.PathValue("id"))
	s.writeAudience(w, r, res, err)
}

func (s *Server) handleRefineAudience(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Instruction string `json:"instruction"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Instruction) == "" || len(req.Instruction) > 500 {
		writeError(w, http.StatusBadRequest, "invalid_request", "instruction must be 1 to 500 characters")
		return
	}
	res, err := s.campaigns.RefineAudience(r.Context(), r.PathValue("id"), req.Instruction)
	s.writeAudience(w, r, res, err)
}

func (s *Server) handleSetRule(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Rule json.RawMessage `json:"rule"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	res, err := s.campaigns.SetRule(r.Context(), r.PathValue("id"), req.Rule)
	s.writeAudience(w, r, res, err)
}

// writeAudience maps the audience step's errors to responses the UI can act on.
func (s *Server) writeAudience(w http.ResponseWriter, r *http.Request, res campaign.AudienceResult, err error) {
	var (
		ve  *segment.ValidationError
		inv *composer.ErrInvalidOutput
		se  *llm.StatusError
	)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, res)
	case errors.As(err, &ve):
		writeRuleError(w, err)
	case errors.As(err, &inv):
		writeProblems(w, http.StatusUnprocessableEntity, "llm_invalid_output",
			"the AI could not produce a valid audience for this brief; try rewording it or edit the rule by hand", inv.Problems)
	case errors.Is(err, llm.ErrRefused):
		writeError(w, http.StatusUnprocessableEntity, "llm_refused", "the AI declined this brief")
	case errors.As(err, &se) && se.Temporary():
		s.log.Warn("llm unavailable", "status", se.StatusCode, "error", err)
		writeError(w, http.StatusBadGateway, "llm_unavailable", "the AI service is unavailable right now; try again shortly")
	case errors.As(err, &se):
		// Retrying will not fix these (bad key, missing workspace, bad request).
		s.log.Error("llm rejected the request", "status", se.StatusCode, "error", err)
		writeError(w, http.StatusBadGateway, "llm_rejected",
			"the AI service rejected the request; check the server's Anthropic API key, workspace and billing")
	case errors.Is(err, campaign.ErrGenerating):
		writeError(w, http.StatusConflict, "generating", err.Error())
	case errors.Is(err, campaign.ErrWrongStatus):
		writeError(w, http.StatusConflict, "wrong_status", err.Error())
	default:
		s.storeError(w, r, err, "campaign")
	}
}

func writeProblems(w http.ResponseWriter, status int, code, message string, problems []string) {
	var body struct {
		Error struct {
			Code     string   `json:"code"`
			Message  string   `json:"message"`
			Problems []string `json:"problems"`
		} `json:"error"`
	}
	body.Error.Code, body.Error.Message, body.Error.Problems = code, message, problems
	writeJSON(w, status, body)
}

// handleStartContent queues message generation and answers 202 at once. The
// text streams to GET /api/campaigns/{id}/events.
func (s *Server) handleStartContent(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Language    string `json:"language"`
		Instruction string `json:"instruction"`
	}
	if r.ContentLength != 0 && !decodeJSON(w, r, &req) {
		return
	}
	if len(req.Instruction) > 500 {
		writeError(w, http.StatusBadRequest, "invalid_request", "instruction must be at most 500 characters")
		return
	}
	c, err := s.campaigns.StartContent(r.Context(), r.PathValue("id"), req.Language, req.Instruction)
	switch {
	case err == nil:
		writeJSON(w, http.StatusAccepted, c)
	case errors.Is(err, campaign.ErrBadLanguage):
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
	case errors.Is(err, campaign.ErrWrongStatus):
		writeError(w, http.StatusConflict, "wrong_status", "generate the audience first; messages can change only before approval")
	default:
		s.storeError(w, r, err, "campaign")
	}
}

// handleSetContent saves messages written or edited by the marketer.
func (s *Server) handleSetContent(w http.ResponseWriter, r *http.Request) {
	var req message.Content
	if !decodeJSON(w, r, &req) {
		return
	}
	c, err := s.campaigns.SetContent(r.Context(), r.PathValue("id"), req)
	var ce *campaign.ContentError
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, c)
	case errors.As(err, &ce):
		writeProblems(w, http.StatusUnprocessableEntity, "invalid_content", "the messages are not valid", ce.Problems)
	case errors.Is(err, campaign.ErrWrongStatus):
		writeError(w, http.StatusConflict, "wrong_status", err.Error())
	default:
		s.storeError(w, r, err, "campaign")
	}
}

var errNoProgress = errors.New("no delivery progress yet")

// handleCampaignEvents streams a campaign's events (status changes, message
// text as it is written, later delivery progress) with server-sent events.
func (s *Server) handleCampaignEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.campaigns.Get(r.Context(), id); err != nil {
		s.storeError(w, r, err, "campaign")
		return
	}
	s.streamTopic(w, r, campaign.Topic(id), streamOptions{
		// The stream ends once nothing more will change: sending is done and the
		// receipts are in (or stopped coming).
		Finished: func(ctx context.Context) (bool, error) {
			c, err := s.campaigns.Get(ctx, id) // cheap check first: runs on every doorbell
			if err != nil || (c.Status != campaign.StatusCompleted && c.Status != campaign.StatusFailed) {
				return false, err
			}
			p, err := s.campaigns.Progress(ctx, id)
			if err != nil {
				return false, err
			}
			return p.Settled(time.Now()), nil
		},
		Snapshot: func(ctx context.Context) (any, error) {
			c, err := s.campaigns.Get(ctx, id)
			if err != nil || c.ApprovedAt == nil {
				return nil, errNoProgress // nothing to show before approval
			}
			return s.campaigns.Progress(ctx, id)
		},
		SnapshotEvery: 500 * time.Millisecond,
	})
}

package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"campaign-composer/internal/segment"
)

// handleSegmentFields lists the fields and operators a rule may use, so the UI
// can render rule chips and their editors.
func (s *Server) handleSegmentFields(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"fields": segment.Fields()})
}

type previewRequest struct {
	Rule json.RawMessage `json:"rule"`
}

type previewResponse struct {
	Rule    segment.Rule    `json:"rule"` // normalized, e.g. "Istanbul" -> "İstanbul"
	Preview segment.Preview `json:"preview"`
}

// handleSegmentPreview validates a rule and returns how many users it matches.
func (s *Server) handleSegmentPreview(w http.ResponseWriter, r *http.Request) {
	var req previewRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	rule, err := segment.Parse(req.Rule)
	if err != nil {
		writeRuleError(w, err)
		return
	}
	q, err := segment.Compile(rule, s.now())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	p, err := segment.PreviewQuery(r.Context(), s.pool, q)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, previewResponse{Rule: rule, Preview: p})
}

// writeRuleError answers 422 with every problem listed, so the UI can show them
// next to the rule.
func writeRuleError(w http.ResponseWriter, err error) {
	var ve *segment.ValidationError
	if !errors.As(err, &ve) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_rule", err.Error())
		return
	}
	var body struct {
		Error struct {
			Code     string   `json:"code"`
			Message  string   `json:"message"`
			Problems []string `json:"problems"`
		} `json:"error"`
	}
	body.Error.Code = "invalid_rule"
	body.Error.Message = "the audience rule is not valid"
	body.Error.Problems = ve.Problems
	writeJSON(w, http.StatusUnprocessableEntity, body)
}

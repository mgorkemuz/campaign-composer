package campaign

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"

	"campaign-composer/internal/events"
	"campaign-composer/internal/segment"
	"campaign-composer/internal/store"
)

// AudienceResult is a campaign with a fresh preview of its audience.
type AudienceResult struct {
	Campaign Campaign        `json:"campaign"`
	Preview  segment.Preview `json:"preview"`
}

// GenerateAudience asks the LLM for a rule from the campaign's brief.
func (s *Service) GenerateAudience(ctx context.Context, id string) (AudienceResult, error) {
	c, err := s.editable(ctx, id)
	if err != nil {
		return AudienceResult{}, err
	}
	llmCtx, cancel := context.WithTimeout(ctx, s.LLMTimeout)
	defer cancel()
	aud, err := s.composer.GenerateAudience(llmCtx, c.Brief)
	if err != nil {
		return AudienceResult{}, err
	}
	return s.saveAudience(ctx, id, aud.Rule, aud.Summary, aud.Assumptions, aud.Model)
}

// RefineAudience changes the current rule following an instruction.
func (s *Service) RefineAudience(ctx context.Context, id, instruction string) (AudienceResult, error) {
	c, err := s.editable(ctx, id)
	if err != nil {
		return AudienceResult{}, err
	}
	current := segment.Rule{Match: segment.MatchAll, Conditions: []segment.Condition{}}
	if c.Rule != nil {
		current = *c.Rule
	}
	llmCtx, cancel := context.WithTimeout(ctx, s.LLMTimeout)
	defer cancel()
	aud, err := s.composer.RefineAudience(llmCtx, c.Brief, current, instruction)
	if err != nil {
		return AudienceResult{}, err
	}
	return s.saveAudience(ctx, id, aud.Rule, aud.Summary, aud.Assumptions, aud.Model)
}

// SetRule stores a rule edited by the marketer. It goes through the same
// validation as the LLM's rules. The LLM's summary no longer describes it, so
// it is cleared.
func (s *Service) SetRule(ctx context.Context, id string, raw json.RawMessage) (AudienceResult, error) {
	rule, err := segment.Parse(raw)
	if err != nil {
		return AudienceResult{}, err
	}
	c, err := s.editable(ctx, id)
	if err != nil {
		return AudienceResult{}, err
	}
	return s.saveAudience(ctx, id, rule, "", []string{"Edited by hand."}, c.LLMModel)
}

func (s *Service) editable(ctx context.Context, id string) (Campaign, error) {
	c, err := s.Get(ctx, id)
	if err != nil {
		return Campaign{}, err
	}
	for _, st := range audienceEditable {
		if c.Status == st {
			return c, nil
		}
	}
	return Campaign{}, ErrWrongStatus
}

// saveAudience previews the rule and stores it with its counts. The update only
// applies while the audience is still editable, so a campaign approved in the
// meantime (e.g. in another tab) is not changed under it.
func (s *Service) saveAudience(ctx context.Context, id string, rule segment.Rule, summary string,
	assumptions []string, model string) (AudienceResult, error) {
	q, err := segment.Compile(rule, s.now())
	if err != nil {
		return AudienceResult{}, err
	}
	var res AudienceResult
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		p, err := segment.PreviewQuery(ctx, tx, q)
		if err != nil {
			return err
		}
		ruleJSON, _ := json.Marshal(rule)
		tag, err := tx.Exec(ctx, `
			UPDATE campaigns SET status = $2, rule = $3, audience_summary = $4, audience_assumptions = $5,
				audience_total = $6, sms_reachable = $7, email_reachable = $8, llm_model = $9, updated_at = now()
			WHERE id = $1 AND status = ANY($10)`,
			id, StatusAudienceReady, ruleJSON, summary, assumptions,
			p.Total, p.SMSReachable, p.EmailReachable, model, audienceEditable)
		if err != nil {
			return store.MapErr(err)
		}
		if tag.RowsAffected() == 0 {
			return ErrWrongStatus
		}
		if _, err := events.Add(ctx, tx, Topic(id), "audience_ready", map[string]any{
			"total": p.Total, "sms_reachable": p.SMSReachable, "email_reachable": p.EmailReachable,
		}); err != nil {
			return err
		}
		res.Preview = p
		return nil
	})
	if err != nil {
		return AudienceResult{}, err
	}
	res.Campaign, err = s.Get(ctx, id)
	return res, err
}

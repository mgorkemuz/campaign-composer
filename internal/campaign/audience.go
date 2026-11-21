package campaign

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"

	"campaign-composer/internal/events"
	"campaign-composer/internal/message"
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
	res, err := s.saveAudience(ctx, c, aud.Rule, aud.Summary, aud.Assumptions, aud.Model)
	if err != nil {
		return res, err
	}
	// The brief already says who and what, so there is nothing to approve in
	// between: queue the messages right away. Doing it here rather than in the
	// browser means the campaign reaches "ready to send" even if the page is
	// closed or stale.
	if res.Campaign.SMS == "" && res.Campaign.ContentState != ContentGenerating {
		if res.Campaign, err = s.StartContent(ctx, id, "", ""); err != nil {
			return res, err
		}
	}
	return res, nil
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
	return s.saveAudience(ctx, c, aud.Rule, aud.Summary, aud.Assumptions, aud.Model)
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
	return s.saveAudience(ctx, c, rule, "", []string{"Edited by hand."}, c.LLMModel)
}

func (s *Service) editable(ctx context.Context, id string) (Campaign, error) {
	c, err := s.Get(ctx, id)
	if err != nil {
		return Campaign{}, err
	}
	if c.ContentState == ContentGenerating {
		return Campaign{}, ErrGenerating
	}
	for _, st := range audienceEditable {
		if c.Status == st {
			return c, nil
		}
	}
	return Campaign{}, ErrWrongStatus
}

// saveAudience previews the rule and stores it with its counts. Existing
// messages are checked again against the new audience: its longest names may
// push an SMS over the limit. The update only applies while the audience is
// still editable and no generation is running.
func (s *Service) saveAudience(ctx context.Context, c Campaign, rule segment.Rule, summary string,
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

		longest, err := s.longest(ctx, tx, rule)
		if err != nil {
			return err
		}
		longestJSON, _ := json.Marshal(longest)

		status, contentState := StatusAudienceReady, c.ContentState
		var contentError, contentCheck any // nil writes NULL
		if len(c.ContentError) > 0 {
			contentError = string(c.ContentError) // no messages to re-check: keep the last error
		}
		if c.SMS != "" && c.Language != nil {
			check, problems := message.Validate(message.Content{Language: *c.Language, SMS: c.SMS,
				EmailSubject: c.EmailSubject, EmailBody: c.EmailBody}, longest)
			contentCheck, _ = json.Marshal(check)
			if len(problems) == 0 {
				status, contentState, contentError = StatusContentReady, ContentReady, nil
			} else {
				contentState = ContentInvalid
				contentError, _ = json.Marshal(map[string]any{"code": "content_invalid", "problems": problems,
					"message": "the messages no longer fit this audience; edit or regenerate them"})
			}
		}

		ruleJSON, _ := json.Marshal(rule)
		tag, err := tx.Exec(ctx, `
			UPDATE campaigns SET status = $2, rule = $3, audience_summary = $4, audience_assumptions = $5,
				audience_total = $6, sms_reachable = $7, email_reachable = $8, llm_model = $9,
				content_state = $10, content_error = $11, content_check = coalesce($12, content_check),
				audience_longest = $14, updated_at = now()
			WHERE id = $1 AND status = ANY($13) AND content_state <> 'generating'`,
			c.ID, status, ruleJSON, summary, assumptions,
			p.Total, p.SMSReachable, p.EmailReachable, model,
			contentState, contentError, contentCheck, audienceEditable, longestJSON)
		if err != nil {
			return store.MapErr(err)
		}
		if tag.RowsAffected() == 0 {
			return ErrWrongStatus
		}
		if _, err := events.Add(ctx, tx, Topic(c.ID), "audience_ready", map[string]any{
			"total": p.Total, "sms_reachable": p.SMSReachable, "email_reachable": p.EmailReachable,
			"content_state": contentState,
		}); err != nil {
			return err
		}
		res.Preview = p
		return nil
	})
	if err != nil {
		return AudienceResult{}, err
	}
	res.Campaign, err = s.Get(ctx, c.ID)
	return res, err
}

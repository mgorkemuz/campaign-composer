// Package campaign is the campaign workflow: brief, audience, (later) content,
// approval and sending. It ties the composer, the segment engine and the
// database together.
package campaign

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"campaign-composer/internal/composer"
	"campaign-composer/internal/events"
	"campaign-composer/internal/message"
	"campaign-composer/internal/segment"
	"campaign-composer/internal/store"
)

const (
	StatusDraft         = "draft"
	StatusAudienceReady = "audience_ready"
	StatusContentReady  = "content_ready"
	StatusApproved      = "approved"
	StatusSending       = "sending"
	StatusCompleted     = "completed"
	StatusFailed        = "failed"
)

// audienceEditable are the statuses in which the audience may still change.
var audienceEditable = []string{StatusDraft, StatusAudienceReady, StatusContentReady}

// Content states (see migration 0004).
const (
	ContentNone       = "none"
	ContentGenerating = "generating"
	ContentReady      = "ready"
	ContentFailed     = "failed"
	ContentInvalid    = "invalid"
)

const MaxBriefLength = 2000

type Campaign struct {
	ID                  string        `json:"id"`
	Brief               string        `json:"brief"`
	Status              string        `json:"status"`
	Rule                *segment.Rule `json:"rule"`
	AudienceSummary     string        `json:"audience_summary"`
	AudienceAssumptions []string      `json:"audience_assumptions"`
	AudienceTotal       *int          `json:"audience_total"`
	SMSReachable        *int          `json:"sms_reachable"`
	EmailReachable      *int          `json:"email_reachable"`
	LLMModel            string        `json:"llm_model"`

	Language     *string         `json:"language"`
	SMS          string          `json:"sms"`
	EmailSubject string          `json:"email_subject"`
	EmailBody    string          `json:"email_body"`
	ContentState string          `json:"content_state"`
	ContentError json.RawMessage `json:"content_error"`
	ContentCheck *message.Check  `json:"content_check"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Topic is the event stream of one campaign.
func Topic(id string) string { return "campaign:" + id }

// ErrWrongStatus means the campaign is past the step being changed.
var ErrWrongStatus = errors.New("the campaign cannot be changed in its current status")

// ErrInvalidBrief means the brief is empty or too long.
var ErrInvalidBrief = fmt.Errorf("the brief must be 1 to %d characters", MaxBriefLength)

type Service struct {
	pool     *pgxpool.Pool
	composer *composer.Composer
	now      func() time.Time
	// LLMTimeout bounds one generation, including retries and the repair round.
	LLMTimeout time.Duration
	// DeliveryMaxAttempts is how many times one message is tried.
	DeliveryMaxAttempts int
}

func NewService(pool *pgxpool.Pool, c *composer.Composer, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{pool: pool, composer: c, now: now, LLMTimeout: 2 * time.Minute,
		DeliveryMaxAttempts: DefaultDeliveryMaxAttempts}
}

func (s *Service) Create(ctx context.Context, brief string) (Campaign, error) {
	brief = strings.TrimSpace(brief)
	if brief == "" || len([]rune(brief)) > MaxBriefLength {
		return Campaign{}, ErrInvalidBrief
	}
	var id string
	if err := s.pool.QueryRow(ctx, `INSERT INTO campaigns (brief) VALUES ($1) RETURNING id::text`, brief).Scan(&id); err != nil {
		return Campaign{}, store.MapErr(err)
	}
	if _, err := events.Add(ctx, s.pool, Topic(id), "created", map[string]string{"status": StatusDraft}); err != nil {
		return Campaign{}, err
	}
	return s.Get(ctx, id)
}

const selectCampaign = `
	SELECT id::text, brief, status, rule, audience_summary, audience_assumptions,
	       audience_total, sms_reachable, email_reachable, llm_model,
	       language, sms_body, email_subject, email_body, content_state, content_error, content_check,
	       created_at, updated_at
	FROM campaigns`

type scanner interface{ Scan(dest ...any) error }

func scan(row scanner) (Campaign, error) {
	var c Campaign
	var rule []byte
	err := row.Scan(&c.ID, &c.Brief, &c.Status, &rule, &c.AudienceSummary, &c.AudienceAssumptions,
		&c.AudienceTotal, &c.SMSReachable, &c.EmailReachable, &c.LLMModel,
		&c.Language, &c.SMS, &c.EmailSubject, &c.EmailBody, &c.ContentState, &c.ContentError, &c.ContentCheck,
		&c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return Campaign{}, store.MapErr(err)
	}
	if rule != nil {
		var r segment.Rule
		if err := json.Unmarshal(rule, &r); err != nil {
			return Campaign{}, err
		}
		c.Rule = &r
	}
	return c, nil
}

func (s *Service) Get(ctx context.Context, id string) (Campaign, error) {
	return scan(s.pool.QueryRow(ctx, selectCampaign+` WHERE id = $1`, id))
}

func (s *Service) List(ctx context.Context, limit int) ([]Campaign, error) {
	rows, err := s.pool.Query(ctx, selectCampaign+` ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, store.MapErr(err)
	}
	defer rows.Close()
	out := []Campaign{}
	for rows.Next() {
		c, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, store.MapErr(rows.Err())
}

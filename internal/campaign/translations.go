package campaign

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"campaign-composer/internal/composer"
	"campaign-composer/internal/events"
	"campaign-composer/internal/llm"
	"campaign-composer/internal/message"
	"campaign-composer/internal/queue"
	"campaign-composer/internal/segment"
	"campaign-composer/internal/store"
)

// JobTranslate is the queue kind for one language's translation.
const JobTranslate = "translate_content"

const (
	TranslationTranslating = "translating"
	TranslationReady       = "ready"
	TranslationFailed      = "failed"
)

// Translation is a campaign's messages in one more language.
type Translation struct {
	Language     string          `json:"language"`
	State        string          `json:"state"`
	Recipients   int             `json:"recipients"`
	SMS          string          `json:"sms"`
	EmailSubject string          `json:"email_subject"`
	EmailBody    string          `json:"email_body"`
	ContentCheck *message.Check  `json:"content_check"`
	Error        json.RawMessage `json:"error"`
	version      int
}

// ErrTranslating means approval must wait for translations still being written.
var ErrTranslating = errors.New("translations are still being written")

type translateJob struct {
	CampaignID string `json:"campaign_id"`
	Language   string `json:"language"`
	Version    int    `json:"version"`
}

// queueTranslations makes sure every other language the reachable audience
// speaks has a translation of the current messages: missing, outdated or failed
// ones are queued; languages no longer in the audience are dropped. Call it in
// the transaction that made the messages ready.
func (s *Service) queueTranslations(ctx context.Context, tx pgx.Tx, id string) error {
	var (
		rule     segment.Rule
		ruleJSON []byte
		primary  *string
		version  int
	)
	if err := tx.QueryRow(ctx, `SELECT rule, language, content_version FROM campaigns WHERE id = $1`, id).
		Scan(&ruleJSON, &primary, &version); err != nil {
		return store.MapErr(err)
	}
	if ruleJSON == nil || primary == nil {
		return nil
	}
	if err := json.Unmarshal(ruleJSON, &rule); err != nil {
		return err
	}
	q, err := segment.Compile(rule, s.now())
	if err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `
		SELECT u.language, count(*) FROM users u
		WHERE `+q.Where+` AND (u.sms_consent OR u.email_consent)
		GROUP BY 1`, q.Args...)
	if err != nil {
		return err
	}
	counts := map[string]int{}
	for rows.Next() {
		var lang string
		var n int
		if err := rows.Scan(&lang, &n); err != nil {
			rows.Close()
			return err
		}
		if lang != *primary {
			counts[lang] = n
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	langs := make([]string, 0, len(counts))
	for l := range counts {
		langs = append(langs, l)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM campaign_translations WHERE campaign_id = $1 AND NOT (language = ANY($2))`,
		id, langs); err != nil {
		return err
	}
	for _, lang := range langs {
		// Only (re)queue when there is no ready translation of these exact messages.
		var queued bool
		err := tx.QueryRow(ctx, `
			INSERT INTO campaign_translations (campaign_id, language, version, state, recipients)
			VALUES ($1, $2, $3, 'translating', $4)
			ON CONFLICT (campaign_id, language) DO UPDATE SET
				version = excluded.version, state = 'translating', recipients = excluded.recipients,
				error = NULL, updated_at = now()
			WHERE campaign_translations.version <> excluded.version OR campaign_translations.state = 'failed'
			RETURNING true`, id, lang, version, counts[lang]).Scan(&queued)
		if errors.Is(store.MapErr(err), store.ErrNotFound) {
			// Up to date: refresh the recipient count only.
			if _, err := tx.Exec(ctx, `UPDATE campaign_translations SET recipients = $3 WHERE campaign_id = $1 AND language = $2`,
				id, lang, counts[lang]); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return store.MapErr(err)
		}
		if _, _, err := queue.Enqueue(ctx, tx, JobTranslate, translateJob{CampaignID: id, Language: lang, Version: version},
			queue.EnqueueOptions{MaxAttempts: 3}); err != nil {
			return err
		}
	}
	return nil
}

// HandleTranslateJob writes one language's version of the messages.
func (s *Service) HandleTranslateJob(ctx context.Context, job queue.Job) error {
	var p translateJob
	if err := json.Unmarshal(job.Payload, &p); err != nil {
		return queue.Permanent(err)
	}
	c, err := s.Get(ctx, p.CampaignID)
	if err != nil {
		return queue.Permanent(err)
	}
	if !s.translationCurrent(ctx, p) || c.Rule == nil || c.Language == nil || !slices.Contains(contentEditable, c.Status) {
		return nil // the messages changed since; a newer job covers them
	}
	longest, err := s.longestFor(ctx, *c.Rule, p.Language)
	if err != nil {
		return err
	}
	gen, err := s.composer.Translate(ctx, composer.TranslateRequest{
		Brief: c.Brief, Target: p.Language, AudienceFacts: segment.Describe(*c.Rule), Longest: longest,
		Source: message.Content{Language: *c.Language, SMS: c.SMS, EmailSubject: c.EmailSubject, EmailBody: c.EmailBody},
	})
	if err != nil {
		var se *llm.StatusError
		if errors.As(err, &se) && se.Temporary() || errors.Is(err, context.DeadlineExceeded) {
			return err // retried by the queue
		}
		s.translationFailed(ctx, p, err)
		return queue.Permanent(err)
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		check, _ := json.Marshal(gen.Check)
		tag, err := tx.Exec(ctx, `
			UPDATE campaign_translations SET state = 'ready', sms = $4, email_subject = $5, email_body = $6,
				content_check = $7, error = NULL, updated_at = now()
			WHERE campaign_id = $1 AND language = $2 AND version = $3`,
			p.CampaignID, p.Language, p.Version, gen.Content.SMS, gen.Content.EmailSubject, gen.Content.EmailBody, check)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		_, err = events.Add(ctx, tx, Topic(p.CampaignID), "translation_ready", map[string]string{"language": p.Language})
		return err
	})
}

// OnTranslateJobFailed records a translation that ran out of attempts.
func (s *Service) OnTranslateJobFailed(ctx context.Context, job queue.Job, err error) {
	var p translateJob
	if json.Unmarshal(job.Payload, &p) == nil {
		s.translationFailed(ctx, p, err)
	}
}

func (s *Service) translationCurrent(ctx context.Context, p translateJob) bool {
	var current bool
	err := s.pool.QueryRow(ctx, `
		SELECT c.content_version = $3 AND t.version = $3
		FROM campaigns c JOIN campaign_translations t ON t.campaign_id = c.id AND t.language = $2
		WHERE c.id = $1`, p.CampaignID, p.Language, p.Version).Scan(&current)
	return err == nil && current
}

func (s *Service) translationFailed(ctx context.Context, p translateJob, cause error) {
	body := map[string]any{"message": "translation failed; these recipients will get the main language"}
	var inv *composer.ErrInvalidOutput
	if errors.As(cause, &inv) {
		body["problems"] = inv.Problems
	}
	bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_ = pgx.BeginFunc(bg, s.pool, func(tx pgx.Tx) error {
		raw, _ := json.Marshal(body)
		tag, err := tx.Exec(bg, `
			UPDATE campaign_translations SET state = 'failed', error = $4, updated_at = now()
			WHERE campaign_id = $1 AND language = $2 AND version = $3`, p.CampaignID, p.Language, p.Version, raw)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		_, err = events.Add(bg, tx, Topic(p.CampaignID), "translation_failed", map[string]string{"language": p.Language})
		return err
	})
}

// longestFor is longest for one language's users in the audience.
func (s *Service) longestFor(ctx context.Context, rule segment.Rule, lang string) (message.Longest, error) {
	q, err := segment.Compile(rule, s.now())
	if err != nil {
		return message.Longest{}, err
	}
	var l message.Longest
	err = s.pool.QueryRow(ctx, fmt.Sprintf(`
		SELECT coalesce(max(char_length(u.first_name)), 0),
		       coalesce(max(char_length(u.last_name)), 0),
		       coalesce(max(char_length(u.city)), 0)
		FROM users u WHERE %s AND u.language = $%d`, q.Where, len(q.Args)+1), append(q.Args, lang)...).
		Scan(&l.FirstName, &l.LastName, &l.City)
	return l, store.MapErr(err)
}

func (s *Service) translations(ctx context.Context, id string) ([]Translation, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT language, state, recipients, sms, email_subject, email_body, content_check, error, version
		FROM campaign_translations WHERE campaign_id = $1 ORDER BY recipients DESC, language`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Translation{}
	for rows.Next() {
		var t Translation
		if err := rows.Scan(&t.Language, &t.State, &t.Recipients, &t.SMS, &t.EmailSubject, &t.EmailBody,
			&t.ContentCheck, &t.Error, &t.version); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

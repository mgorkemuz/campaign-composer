package campaign

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
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

// JobGenerateContent is the queue kind for message generation.
const JobGenerateContent = "generate_content"

// contentEditable are the statuses in which messages may be (re)generated or edited.
var contentEditable = []string{StatusAudienceReady, StatusContentReady}

// ContentError lists why hand-edited content was rejected.
type ContentError struct {
	Problems []string `json:"problems"`
}

func (e *ContentError) Error() string { return "invalid content: " + strings.Join(e.Problems, "; ") }

// ErrBadLanguage means an unknown message language was requested.
var ErrBadLanguage = fmt.Errorf("language must be one of %s, or empty for the brief's language",
	strings.Join(message.Languages, ", "))

// ErrGenerating means messages are being generated, so the audience cannot
// change until the job finishes.
var ErrGenerating = errors.New("messages are being generated; wait for them to finish")

type contentJob struct {
	CampaignID  string `json:"campaign_id"`
	Version     int    `json:"version"`
	Language    string `json:"language,omitempty"`
	Instruction string `json:"instruction,omitempty"`
}

// StartContent queues message generation. It returns at once; the messages
// stream to the campaign's event stream as they are written.
func (s *Service) StartContent(ctx context.Context, id, language, instruction string) (Campaign, error) {
	if language != "" && !slices.Contains(message.Languages, language) {
		return Campaign{}, ErrBadLanguage
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var version int
		err := tx.QueryRow(ctx, `
			UPDATE campaigns SET content_version = content_version + 1, content_state = 'generating',
				content_error = NULL, status = $2, updated_at = now()
			WHERE id = $1 AND status = ANY($3) AND rule IS NOT NULL
			RETURNING content_version`, id, StatusAudienceReady, contentEditable).Scan(&version)
		if errors.Is(store.MapErr(err), store.ErrNotFound) {
			if _, gerr := s.Get(ctx, id); gerr != nil {
				return gerr
			}
			return ErrWrongStatus
		}
		if err != nil {
			return store.MapErr(err)
		}
		job := contentJob{CampaignID: id, Version: version, Language: language, Instruction: instruction}
		if _, _, err := queue.Enqueue(ctx, tx, JobGenerateContent, job, queue.EnqueueOptions{
			DedupKey: fmt.Sprintf("content:%s:%d", id, version), MaxAttempts: 3,
		}); err != nil {
			return err
		}
		_, err = events.Add(ctx, tx, Topic(id), "content_started", map[string]int{"version": version})
		return err
	})
	if err != nil {
		return Campaign{}, err
	}
	return s.Get(ctx, id)
}

// HandleContentJob is the queue handler for JobGenerateContent.
func (s *Service) HandleContentJob(ctx context.Context, job queue.Job) error {
	var p contentJob
	if err := json.Unmarshal(job.Payload, &p); err != nil {
		return queue.Permanent(err)
	}
	c, err := s.Get(ctx, p.CampaignID)
	if err != nil {
		return queue.Permanent(err)
	}
	if !s.isCurrent(ctx, p) {
		return nil // a newer generation or a hand edit replaced this one
	}
	if c.Rule == nil {
		return queue.Permanent(errors.New("campaign has no audience"))
	}
	longest, err := s.longest(ctx, s.pool, *c.Rule)
	if err != nil {
		return err
	}

	deltas := newDeltaWriter(s.pool, Topic(p.CampaignID))
	gen, err := s.composer.GenerateContent(ctx, composer.ContentRequest{
		Brief: c.Brief, AudienceSummary: c.AudienceSummary, Language: p.Language,
		Longest: longest, Instruction: p.Instruction,
	}, deltas.add)
	deltas.flush()
	if err != nil {
		var se *llm.StatusError
		if errors.As(err, &se) && se.Temporary() || errors.Is(err, context.DeadlineExceeded) {
			return err // the queue retries; OnContentJobFailed records the final failure
		}
		s.contentFailed(ctx, p, err)
		return queue.Permanent(err)
	}

	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		check, _ := json.Marshal(gen.Check)
		tag, err := tx.Exec(ctx, `
			UPDATE campaigns SET language = $3, sms_body = $4, email_subject = $5, email_body = $6,
				content_state = 'ready', content_error = NULL, content_check = $7, status = $8,
				llm_model = $9, updated_at = now()
			WHERE id = $1 AND content_version = $2 AND status = ANY($10)`,
			p.CampaignID, p.Version, gen.Content.Language, gen.Content.SMS, gen.Content.EmailSubject,
			gen.Content.EmailBody, check, StatusContentReady, gen.Model, contentEditable)
		if err != nil || tag.RowsAffected() == 0 {
			return store.MapErr(err) // nil when superseded: nothing to save
		}
		_, err = events.Add(ctx, tx, Topic(p.CampaignID), "content_ready", gen)
		return err
	})
	if err != nil {
		return err
	}
	return nil
}

// OnContentJobFailed records a generation that ran out of attempts.
func (s *Service) OnContentJobFailed(ctx context.Context, job queue.Job, err error) {
	var p contentJob
	if json.Unmarshal(job.Payload, &p) == nil {
		s.contentFailed(ctx, p, err)
	}
}

func (s *Service) isCurrent(ctx context.Context, p contentJob) bool {
	var v int
	err := s.pool.QueryRow(ctx, `SELECT content_version FROM campaigns WHERE id = $1`, p.CampaignID).Scan(&v)
	return err == nil && v == p.Version
}

// contentFailed stores why generation failed, so the UI can show it.
func (s *Service) contentFailed(ctx context.Context, p contentJob, cause error) {
	body := map[string]any{"code": "llm_error", "message": "message generation failed; try again"}
	var inv *composer.ErrInvalidOutput
	var se *llm.StatusError
	switch {
	case errors.As(cause, &inv):
		body = map[string]any{"code": "llm_invalid_output", "problems": inv.Problems,
			"message": "the AI could not write messages within the limits; try again or write them by hand"}
	case errors.Is(cause, llm.ErrRefused):
		body = map[string]any{"code": "llm_refused", "message": "the AI declined this brief"}
	case errors.As(cause, &se) && !se.Temporary():
		body = map[string]any{"code": "llm_rejected",
			"message": "the AI service rejected the request; check the server's Anthropic API key, workspace and billing"}
	case errors.As(cause, &se) || errors.Is(cause, queue.ErrLeaseExpired):
		body = map[string]any{"code": "llm_unavailable", "message": "the AI service is unavailable right now; try again shortly"}
	}
	// The job's context may already be over (e.g. it timed out); recording the
	// failure must still happen.
	bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_ = pgx.BeginFunc(bg, s.pool, func(tx pgx.Tx) error {
		raw, _ := json.Marshal(body)
		tag, err := tx.Exec(bg, `
			UPDATE campaigns SET content_state = 'failed', content_error = $3, updated_at = now()
			WHERE id = $1 AND content_version = $2`, p.CampaignID, p.Version, raw)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		_, err = events.Add(bg, tx, Topic(p.CampaignID), "content_failed", body)
		return err
	})
}

// SetContent stores messages written or edited by the marketer, after the same
// checks generated messages pass. It supersedes any generation in progress.
func (s *Service) SetContent(ctx context.Context, id string, content message.Content) (Campaign, error) {
	c, err := s.Get(ctx, id)
	if err != nil {
		return Campaign{}, err
	}
	if !slices.Contains(contentEditable, c.Status) || c.Rule == nil {
		return Campaign{}, ErrWrongStatus
	}
	longest, err := s.longest(ctx, s.pool, *c.Rule)
	if err != nil {
		return Campaign{}, err
	}
	check, problems := message.Validate(content, longest)
	if len(problems) > 0 {
		return Campaign{}, &ContentError{Problems: problems}
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		checkJSON, _ := json.Marshal(check)
		tag, err := tx.Exec(ctx, `
			UPDATE campaigns SET language = $2, sms_body = $3, email_subject = $4, email_body = $5,
				content_state = 'ready', content_error = NULL, content_check = $6, status = $7,
				content_version = content_version + 1, updated_at = now()
			WHERE id = $1 AND status = ANY($8)`,
			id, content.Language, content.SMS, content.EmailSubject, content.EmailBody,
			checkJSON, StatusContentReady, contentEditable)
		if err != nil {
			return store.MapErr(err)
		}
		if tag.RowsAffected() == 0 {
			return ErrWrongStatus
		}
		_, err = events.Add(ctx, tx, Topic(id), "content_ready",
			composer.GeneratedContent{Content: content, Check: check, Model: "edited by hand"})
		return err
	})
	if err != nil {
		return Campaign{}, err
	}
	return s.Get(ctx, id)
}

// longest finds the longest variable values in a rule's audience.
func (s *Service) longest(ctx context.Context, db store.DBTX, rule segment.Rule) (message.Longest, error) {
	q, err := segment.Compile(rule, s.now())
	if err != nil {
		return message.Longest{}, err
	}
	var l message.Longest
	err = db.QueryRow(ctx, `
		SELECT coalesce(max(char_length(u.first_name)), 0),
		       coalesce(max(char_length(u.last_name)), 0),
		       coalesce(max(char_length(u.city)), 0)
		FROM users u WHERE `+q.Where, q.Args...).Scan(&l.FirstName, &l.LastName, &l.City)
	return l, store.MapErr(err)
}

// deltaWriter batches streamed message text into notifications of at most
// ~1 KB, flushed every 100 ms or when the field or attempt changes. Each
// notification is a JSON object the browser can apply directly.
type deltaWriter struct {
	db    store.DBTX
	topic string

	mu      sync.Mutex
	attempt int
	field   string
	buf     strings.Builder
	last    time.Time
}

func newDeltaWriter(db store.DBTX, topic string) *deltaWriter {
	return &deltaWriter{db: db, topic: topic, last: time.Now()}
}

func (d *deltaWriter) add(attempt int, field, text string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if attempt != d.attempt || field != d.field {
		d.flushLocked()
		d.attempt, d.field = attempt, field
	}
	d.buf.WriteString(text)
	if d.buf.Len() >= 400 || time.Since(d.last) > 100*time.Millisecond {
		d.flushLocked()
	}
}

func (d *deltaWriter) flush() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.flushLocked()
}

func (d *deltaWriter) flushLocked() {
	text := d.buf.String()
	d.buf.Reset()
	d.last = time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, part := range splitRunes(text, 300) {
		payload, _ := json.Marshal(map[string]any{"attempt": d.attempt, "field": d.field, "text": part})
		_ = events.Delta(ctx, d.db, d.topic, string(payload)) // best effort: the final text is stored
	}
}

// splitRunes cuts text into pieces of at most n characters. 300 characters stay
// under the NOTIFY size limit even when JSON escapes every one of them.
func splitRunes(text string, n int) []string {
	var parts []string
	r := []rune(text)
	for len(r) > 0 {
		k := min(n, len(r))
		parts = append(parts, string(r[:k]))
		r = r[k:]
	}
	return parts
}

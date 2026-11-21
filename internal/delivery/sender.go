package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/time/rate"

	"campaign-composer/internal/events"
	"campaign-composer/internal/message"
	"campaign-composer/internal/queue"
)

const (
	StatusQueued      = "queued"
	StatusSending     = "sending"
	StatusRetrying    = "retrying"
	StatusSent        = "sent"
	StatusDelivered   = "delivered"
	StatusUndelivered = "undelivered"
	StatusFailed      = "failed"
)

// Channels and their queue job kinds.
var Channels = []string{"sms", "email"}

func JobKind(channel string) string { return "send_" + channel }

// Topic must match campaign.Topic; it is repeated here to keep the packages apart.
func campaignTopic(id string) string { return "campaign:" + id }

// Observer receives delivery outcomes for metrics. Nil is fine.
type Observer interface {
	ProviderCall(channel string, status int, d time.Duration)
	DeliveryFinished(channel, status string)
}

type Sender struct {
	pool     *pgxpool.Pool
	provider Provider
	log      *slog.Logger
	limiters map[string]*rate.Limiter
	ringer   *ringer
	observer Observer
}

// NewSender limits each channel to perSecond sends. The burst is 1, so the
// limit also holds over any short window, not only on average.
func NewSender(pool *pgxpool.Pool, provider Provider, perSecond float64, log *slog.Logger, obs Observer) *Sender {
	s := &Sender{pool: pool, provider: provider, log: log, limiters: map[string]*rate.Limiter{},
		ringer: newRinger(pool, 250*time.Millisecond), observer: obs}
	for _, ch := range Channels {
		s.limiters[ch] = rate.NewLimiter(rate.Limit(perSecond), 1)
	}
	return s
}

type jobPayload struct {
	DeliveryID int64 `json:"delivery_id"`
}

// Handle sends one delivery. It is the queue handler for both channels.
func (s *Sender) Handle(ctx context.Context, job queue.Job) error {
	var p jobPayload
	if err := json.Unmarshal(job.Payload, &p); err != nil {
		return queue.Permanent(err)
	}
	var (
		status, channel, campaignID string
		round                       int
		r                           message.Recipient
		phone, email                string
		content                     message.Content
	)
	err := s.pool.QueryRow(ctx, `
		SELECT d.status, d.channel, d.campaign_id::text, d.resend_count,
		       u.first_name, u.last_name, u.city, u.phone, u.email,
		       c.sms_body, c.email_subject, c.email_body
		FROM deliveries d
		JOIN users u ON u.id = d.user_id
		JOIN campaigns c ON c.id = d.campaign_id
		WHERE d.id = $1`, p.DeliveryID).Scan(&status, &channel, &campaignID, &round,
		&r.FirstName, &r.LastName, &r.City, &phone, &email,
		&content.SMS, &content.EmailSubject, &content.EmailBody)
	if err != nil {
		return err
	}
	switch status {
	case StatusSent, StatusDelivered, StatusUndelivered, StatusFailed:
		return nil // already finished: a duplicate job must not send again
	}
	if _, err := s.pool.Exec(ctx, `
		UPDATE deliveries SET status = 'sending', attempts = attempts + 1, updated_at = now()
		WHERE id = $1 AND status IN ('queued', 'retrying', 'sending')`, p.DeliveryID); err != nil {
		return err
	}
	s.ringer.ring(campaignTopic(campaignID))

	m := Message{Channel: channel, Reference: Reference(p.DeliveryID, round),
		IdempotencyKey: "delivery-" + Reference(p.DeliveryID, round)}
	if channel == "sms" {
		m.To, m.Body = phone, message.Render(content.SMS, r)
	} else {
		m.To, m.Subject, m.Body = email, message.Render(content.EmailSubject, r), message.Render(content.EmailBody, r)
	}

	if err := s.limiters[channel].Wait(ctx); err != nil {
		return err // the job timed out waiting its turn; it will be retried
	}
	start := time.Now()
	msgID, err := s.provider.Send(ctx, m)
	s.observe(channel, err, time.Since(start))

	if err == nil {
		// A receipt may already have arrived (it can beat the response): keep it.
		_, err = s.pool.Exec(ctx, `
			UPDATE deliveries SET
				status = CASE WHEN status IN ('delivered', 'undelivered') THEN status ELSE 'sent' END,
				provider_message_id = $2, sent_at = now(), last_error = '', updated_at = now()
			WHERE id = $1`, p.DeliveryID, msgID)
		if err != nil {
			return err
		}
		s.finished(ctx, campaignID, channel, StatusSent)
		return nil
	}

	var pe *ProviderError
	if errors.As(err, &pe) && !pe.Temporary() {
		s.markFailed(ctx, p.DeliveryID, campaignID, channel, err, true)
		return queue.Permanent(err)
	}
	if _, uerr := s.pool.Exec(ctx, `
		UPDATE deliveries SET status = 'retrying', last_error = $2, updated_at = now()
		WHERE id = $1 AND status = 'sending'`, p.DeliveryID, err.Error()); uerr != nil {
		s.log.Error("mark delivery retrying", "delivery", p.DeliveryID, "error", uerr)
	}
	s.ringer.ring(campaignTopic(campaignID))
	if pe != nil && pe.RetryAfter > 0 {
		return queue.RetryAfter(err, pe.RetryAfter)
	}
	return err
}

// OnFailed marks a delivery failed once the queue has given up on it.
func (s *Sender) OnFailed(ctx context.Context, job queue.Job, err error) {
	var p jobPayload
	if json.Unmarshal(job.Payload, &p) != nil {
		return
	}
	var campaignID, channel string
	if s.pool.QueryRow(ctx, `SELECT campaign_id::text, channel FROM deliveries WHERE id = $1`, p.DeliveryID).
		Scan(&campaignID, &channel) != nil {
		return
	}
	s.markFailed(ctx, p.DeliveryID, campaignID, channel, err, false)
}

// markFailed records a delivery the pipeline gave up on. permanent means the
// provider rejected it for good, so a resend would not help.
func (s *Sender) markFailed(ctx context.Context, id int64, campaignID, channel string, cause error, permanent bool) {
	// A receipt that already said delivered wins over our own give-up: the
	// provider knows best whether the message arrived. An already failed row is
	// left alone: the handler and the queue's OnFailed both report the same
	// permanent failure, and it must be counted once.
	tag, err := s.pool.Exec(ctx, `
		UPDATE deliveries SET status = 'failed', last_error = $2, permanent = $3, updated_at = now()
		WHERE id = $1 AND status NOT IN ('sent', 'delivered', 'undelivered', 'failed')`, id, cause.Error(), permanent)
	if err != nil {
		s.log.Error("mark delivery failed", "delivery", id, "error", err)
		return
	}
	if tag.RowsAffected() == 1 {
		s.finished(ctx, campaignID, channel, StatusFailed)
	}
}

// Reference is what the provider echoes back in receipts: the delivery ID, plus
// ".<round>" for resends ("123", "123.1"). Round 0 keeps the plain ID.
func Reference(id int64, round int) string {
	if round == 0 {
		return strconv.FormatInt(id, 10)
	}
	return fmt.Sprintf("%d.%d", id, round)
}

// Ring wakes a topic's listeners, throttled (see ringer).
func (s *Sender) Ring(topic string) { s.ringer.ring(topic) }

// finished records a delivery leaving the send pipeline and completes the
// campaign once none are left in it.
func (s *Sender) finished(ctx context.Context, campaignID, channel, status string) {
	if s.observer != nil {
		s.observer.DeliveryFinished(channel, status)
	}
	s.ringer.ring(campaignTopic(campaignID))
	if err := CompleteIfDone(ctx, s.pool, campaignID); err != nil {
		s.log.Error("complete campaign", "campaign", campaignID, "error", err)
	}
}

func (s *Sender) observe(channel string, err error, d time.Duration) {
	if s.observer == nil {
		return
	}
	status := 202
	var pe *ProviderError
	if errors.As(err, &pe) {
		status = pe.StatusCode
	} else if err != nil {
		status = 0
	}
	s.observer.ProviderCall(channel, status, d)
}

// CompleteIfDone moves a sending campaign to completed when none of its
// deliveries are still queued, sending or retrying. Receipts may still arrive
// afterwards; they update deliveries, not the campaign's status.
func CompleteIfDone(ctx context.Context, pool *pgxpool.Pool, campaignID string) error {
	var done bool
	err := pool.QueryRow(ctx, `
		WITH done AS (
			UPDATE campaigns SET status = 'completed', completed_at = now(), updated_at = now()
			WHERE id = $1 AND status = 'sending'
			  AND NOT EXISTS (SELECT 1 FROM deliveries
			                  WHERE campaign_id = $1 AND status IN ('queued', 'sending', 'retrying'))
			RETURNING id)
		SELECT EXISTS (SELECT 1 FROM done)`, campaignID).Scan(&done)
	if err != nil || !done {
		return err
	}
	_, err = events.Add(ctx, pool, campaignTopic(campaignID), "completed", map[string]string{"status": "completed"})
	return err
}

// ringer limits doorbells per topic to one every `every`, with a trailing ring
// so the last change is never lost. Thousands of deliveries then cost a few
// notifications a second, not one each.
type ringer struct {
	pool  *pgxpool.Pool
	every time.Duration

	mu      sync.Mutex
	last    map[string]time.Time
	pending map[string]bool
}

func newRinger(pool *pgxpool.Pool, every time.Duration) *ringer {
	return &ringer{pool: pool, every: every, last: map[string]time.Time{}, pending: map[string]bool{}}
}

func (r *ringer) ring(topic string) {
	r.mu.Lock()
	if r.pending[topic] {
		r.mu.Unlock()
		return
	}
	since := time.Since(r.last[topic])
	if since >= r.every {
		r.last[topic] = time.Now()
		r.mu.Unlock()
		r.send(topic)
		return
	}
	r.pending[topic] = true
	r.mu.Unlock()
	time.AfterFunc(r.every-since, func() {
		r.mu.Lock()
		r.pending[topic] = false
		r.last[topic] = time.Now()
		r.mu.Unlock()
		r.send(topic)
	})
}

func (r *ringer) send(topic string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = events.Ring(ctx, r.pool, topic)
}

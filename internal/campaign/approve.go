package campaign

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"campaign-composer/internal/delivery"
	"campaign-composer/internal/events"
	"campaign-composer/internal/message"
	"campaign-composer/internal/queue"
	"campaign-composer/internal/segment"
)

// DefaultDeliveryMaxAttempts bounds retries per message: with backoff (1 s
// base, 60 s cap) a message keeps trying for about two minutes before it counts
// as failed.
const DefaultDeliveryMaxAttempts = 6

// consentColumn is fixed per channel; it never comes from input.
var consentColumn = map[string]string{"sms": "sms_consent", "email": "email_consent"}

// ErrNotReady means the campaign has no valid messages yet.
var ErrNotReady = errors.New("the campaign needs an audience and valid messages before approval")

type ApproveResult struct {
	Campaign Campaign `json:"campaign"`
	SMS      int      `json:"sms"`
	Email    int      `json:"email"`
}

// Approve freezes the campaign and queues one delivery per consenting
// recipient and channel. Everything happens in one transaction: the deliveries,
// their send jobs and the status change commit together or not at all.
func (s *Service) Approve(ctx context.Context, id string) (ApproveResult, error) {
	var res ApproveResult
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		c, err := scan(tx.QueryRow(ctx, selectCampaign+` WHERE id = $1 FOR UPDATE`, id))
		if err != nil {
			return err
		}
		if c.Status != StatusContentReady {
			if c.Status == StatusSending || c.Status == StatusCompleted || c.Status == StatusApproved {
				return ErrWrongStatus // already approved: approving again does nothing
			}
			return ErrNotReady
		}
		if c.ContentState != ContentReady || c.Rule == nil || c.Language == nil {
			return ErrNotReady
		}

		// Check the messages once more against the audience as it is now:
		// "within 30 days" rules shift as time passes.
		longest, err := s.longest(ctx, tx, *c.Rule)
		if err != nil {
			return err
		}
		if _, problems := message.Validate(message.Content{Language: *c.Language, SMS: c.SMS,
			EmailSubject: c.EmailSubject, EmailBody: c.EmailBody}, longest); len(problems) > 0 {
			return &ContentError{Problems: problems}
		}

		q, err := segment.Compile(*c.Rule, s.now())
		if err != nil {
			return err
		}
		q = q.WithOffset(1) // $1 is the campaign ID
		args := append([]any{id}, q.Args...)
		for _, ch := range delivery.Channels {
			// ch and its consent column come from fixed lists, never from input.
			if _, err := tx.Exec(ctx, `
				INSERT INTO deliveries (campaign_id, user_id, channel)
				SELECT $1, u.id, '`+ch+`' FROM users u
				WHERE `+q.Where+` AND u.`+consentColumn[ch]+`
				ON CONFLICT (campaign_id, user_id, channel) DO NOTHING`, args...); err != nil {
				return err
			}
		}
		// One send job per delivery, keyed by the delivery so it can never be
		// queued twice.
		if _, err := tx.Exec(ctx, `
			INSERT INTO jobs (kind, dedup_key, payload, max_attempts)
			SELECT 'send_' || d.channel, 'delivery:' || d.id, jsonb_build_object('delivery_id', d.id), $2
			FROM deliveries d WHERE d.campaign_id = $1
			ON CONFLICT (dedup_key) DO NOTHING`, id, s.DeliveryMaxAttempts); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FILTER (WHERE channel = 'sms'), count(*) FILTER (WHERE channel = 'email')
			FROM deliveries WHERE campaign_id = $1`, id).Scan(&res.SMS, &res.Email); err != nil {
			return err
		}

		status := StatusSending
		if res.SMS+res.Email == 0 {
			status = StatusCompleted // nobody to send to
		}
		if _, err := tx.Exec(ctx, `
			UPDATE campaigns SET status = $2, approved_at = now(), updated_at = now(),
				completed_at = CASE WHEN $2 = 'completed' THEN now() END
			WHERE id = $1`, id, status); err != nil {
			return err
		}
		for _, ch := range delivery.Channels {
			if err := events.Ring(ctx, tx, queue.Topic(delivery.JobKind(ch))); err != nil {
				return err
			}
		}
		_, err = events.Add(ctx, tx, Topic(id), "approved", map[string]int{"sms": res.SMS, "email": res.Email})
		return err
	})
	if err != nil {
		return ApproveResult{}, err
	}
	res.Campaign, err = s.Get(ctx, id)
	return res, err
}

// DeliveryCounts is how many deliveries a campaign has per channel and status.
type DeliveryCounts map[string]map[string]int

func (s *Service) DeliveryCounts(ctx context.Context, id string) (DeliveryCounts, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT channel, status, count(*) FROM deliveries WHERE campaign_id = $1 GROUP BY 1, 2`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := DeliveryCounts{"sms": {}, "email": {}}
	for rows.Next() {
		var ch, st string
		var n int
		if err := rows.Scan(&ch, &st, &n); err != nil {
			return nil, err
		}
		out[ch][st] = n
	}
	return out, rows.Err()
}

type DeliveryRow struct {
	ID        int64  `json:"id"`
	Channel   string `json:"channel"`
	Status    string `json:"status"`
	Attempts  int    `json:"attempts"`
	LastError string `json:"last_error"`
	FirstName string `json:"first_name"`
	City      string `json:"city"`
	Permanent bool   `json:"permanent"` // rejected for good: resend skips it
	Round     int    `json:"round"`     // 0 first send, 1+ resends
}

// Deliveries lists a campaign's deliveries, optionally of one status, newest
// change first.
func (s *Service) Deliveries(ctx context.Context, id, status string, limit int) ([]DeliveryRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT d.id, d.channel, d.status, d.attempts, d.last_error, u.first_name, u.city, d.permanent, d.resend_count
		FROM deliveries d JOIN users u ON u.id = d.user_id
		WHERE d.campaign_id = $1 AND ($2 = '' OR d.status = $2)
		ORDER BY d.updated_at DESC, d.id LIMIT $3`, id, status, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DeliveryRow{}
	for rows.Next() {
		var r DeliveryRow
		if err := rows.Scan(&r.ID, &r.Channel, &r.Status, &r.Attempts, &r.LastError, &r.FirstName, &r.City, &r.Permanent, &r.Round); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

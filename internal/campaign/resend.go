package campaign

import (
	"context"

	"github.com/jackc/pgx/v5"

	"campaign-composer/internal/delivery"
	"campaign-composer/internal/events"
	"campaign-composer/internal/queue"
)

// resendable selects deliveries worth sending again: the provider reported them
// undelivered, or we gave up after temporary errors. Permanent rejections (a
// malformed number or email) would fail the same way, so they are left out.
const resendable = `(status = 'undelivered' OR (status = 'failed' AND NOT permanent))`

// ResendFailed starts a new round for every resendable delivery of a finished
// campaign and returns how many. Each delivery's round number goes up, so its
// provider reference and idempotency key are new: the resend is sent exactly
// once, and a late receipt from the earlier round cannot change it.
func (s *Service) ResendFailed(ctx context.Context, id string) (int, error) {
	var n int
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var status string
		if err := tx.QueryRow(ctx, `SELECT status FROM campaigns WHERE id = $1 FOR UPDATE`, id).Scan(&status); err != nil {
			return mapNotFound(err)
		}
		if status != StatusCompleted {
			return ErrWrongStatus // still sending, or not approved yet
		}
		tag, err := tx.Exec(ctx, `
			UPDATE deliveries SET status = 'queued', attempts = 0, last_error = '', provider_message_id = NULL,
				sent_at = NULL, receipt_at = NULL, resend_count = resend_count + 1, updated_at = now()
			WHERE campaign_id = $1 AND `+resendable, id)
		if err != nil {
			return err
		}
		if n = int(tag.RowsAffected()); n == 0 {
			return nil
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO jobs (kind, dedup_key, payload, max_attempts)
			SELECT 'send_' || d.channel, 'delivery:' || d.id || ':' || d.resend_count,
			       jsonb_build_object('delivery_id', d.id), $2
			FROM deliveries d WHERE d.campaign_id = $1 AND d.status = 'queued'
			ON CONFLICT (dedup_key) DO NOTHING`, id, s.DeliveryMaxAttempts); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE campaigns SET status = 'sending', completed_at = NULL, updated_at = now() WHERE id = $1`, id); err != nil {
			return err
		}
		for _, ch := range delivery.Channels {
			if err := events.Ring(ctx, tx, queue.Topic(delivery.JobKind(ch))); err != nil {
				return err
			}
		}
		_, err = events.Add(ctx, tx, Topic(id), "resend", map[string]int{"count": n})
		return err
	})
	return n, err
}

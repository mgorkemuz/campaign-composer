package campaign

import (
	"context"

	"github.com/jackc/pgx/v5"

	"campaign-composer/internal/events"
)

// CancelSchedule takes a scheduled campaign back to "ready to send": its queued
// send jobs and deliveries are removed. It fails once any message has started.
func (s *Service) CancelSchedule(ctx context.Context, id string) (Campaign, error) {
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var status string
		if err := tx.QueryRow(ctx, `SELECT status FROM campaigns WHERE id = $1 FOR UPDATE`, id).Scan(&status); err != nil {
			return mapNotFound(err)
		}
		if status != StatusScheduled {
			return ErrWrongStatus
		}
		var started bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM jobs j JOIN deliveries d ON j.dedup_key = 'delivery:' || d.id
			               WHERE d.campaign_id = $1 AND j.status <> 'queued')`, id).Scan(&started); err != nil {
			return err
		}
		if started {
			return ErrWrongStatus // the send time has come; too late to cancel
		}
		if _, err := tx.Exec(ctx, `
			DELETE FROM jobs WHERE dedup_key IN (SELECT 'delivery:' || id FROM deliveries WHERE campaign_id = $1)`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM deliveries WHERE campaign_id = $1`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE campaigns SET status = $2, approved_at = NULL, scheduled_at = NULL, updated_at = now()
			WHERE id = $1`, id, StatusContentReady); err != nil {
			return err
		}
		_, err := events.Add(ctx, tx, Topic(id), "unscheduled", map[string]string{"status": StatusContentReady})
		return err
	})
	if err != nil {
		return Campaign{}, err
	}
	return s.Get(ctx, id)
}

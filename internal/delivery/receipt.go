package delivery

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"campaign-composer/internal/store"
)

// Receipt is the provider's delivery report, sent to our webhook.
type Receipt struct {
	MessageID string    `json:"message_id"`
	Reference string    `json:"reference"` // our delivery ID
	Channel   string    `json:"channel"`
	Status    string    `json:"status"` // delivered | undelivered
	Timestamp time.Time `json:"timestamp"`
}

var ErrBadReceipt = errors.New("receipt has an unknown status or reference")

// parseReference reads "123" (round 0) or "123.2" (round 2).
func parseReference(ref string) (id int64, round int, err error) {
	idPart, roundPart, resend := strings.Cut(ref, ".")
	if id, err = strconv.ParseInt(idPart, 10, 64); err != nil {
		return 0, 0, err
	}
	if resend {
		if round, err = strconv.Atoi(roundPart); err != nil || round < 1 {
			return 0, 0, ErrBadReceipt
		}
	}
	return id, round, nil
}

// ApplyReceipt records a receipt. It reports whether anything changed.
//
// Receipts arrive late, twice, or even before our send call has returned, so
// the update only moves forward: once a delivery is delivered or undelivered,
// later receipts change nothing. A receipt that says delivered also overrides
// our own "failed" (we gave up, but the provider had accepted the message and
// its answer was lost). The delivery is found by our reference, so a receipt
// that beats the send response still lands; the message ID must match if we
// already know it.
func ApplyReceipt(ctx context.Context, pool *pgxpool.Pool, r Receipt) (bool, error) {
	id, round, err := parseReference(r.Reference)
	if err != nil || (r.Status != StatusDelivered && r.Status != StatusUndelivered) || r.MessageID == "" {
		return false, ErrBadReceipt
	}
	var campaignID string
	err = pool.QueryRow(ctx, `
		UPDATE deliveries SET status = $2, receipt_at = $3, updated_at = now(),
			provider_message_id = coalesce(provider_message_id, $4)
		WHERE id = $1 AND resend_count = $5 AND status NOT IN ('delivered', 'undelivered')
		  AND (provider_message_id IS NULL OR provider_message_id = $4)
		RETURNING campaign_id::text`, id, r.Status, r.Timestamp, r.MessageID, round).Scan(&campaignID)
	if errors.Is(store.MapErr(err), store.ErrNotFound) {
		return false, nil // duplicate, late, or not ours: nothing to do
	}
	if err != nil {
		return false, store.MapErr(err)
	}
	// The receipt may have finished a delivery that was still retrying.
	return true, CompleteIfDone(ctx, pool, campaignID)
}

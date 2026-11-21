package campaign

import (
	"context"
	"time"
)

// Grid codes: one character per delivery, in delivery order. The dashboard
// draws one square per character, coloured by status.
var GridCodes = map[string]string{
	"queued": "q", "sending": "s", "retrying": "r", "sent": "t",
	"delivered": "d", "undelivered": "u", "failed": "f",
}

// Progress is a live picture of a campaign's sending.
type Progress struct {
	Status          string             `json:"status"`
	Counts          DeliveryCounts     `json:"counts"`
	Grid            map[string]string  `json:"grid"`
	Throughput      map[string]float64 `json:"throughput"` // accepted per second, last 5 s
	Retries         int                `json:"retries"`    // attempts beyond the first, all deliveries
	PendingReceipts int                `json:"pending_receipts"`
	CompletedAt     *time.Time         `json:"completed_at"`
}

const throughputWindow = 5 // seconds

// Progress reads the campaign's delivery state in one pass.
func (s *Service) Progress(ctx context.Context, id string) (Progress, error) {
	p := Progress{
		Counts:     DeliveryCounts{"sms": {}, "email": {}},
		Grid:       map[string]string{"sms": "", "email": ""},
		Throughput: map[string]float64{"sms": 0, "email": 0},
	}
	if err := s.pool.QueryRow(ctx, `SELECT status, completed_at FROM campaigns WHERE id = $1`, id).
		Scan(&p.Status, &p.CompletedAt); err != nil {
		return Progress{}, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT channel,
		       string_agg(CASE status
		           WHEN 'queued' THEN 'q' WHEN 'sending' THEN 's' WHEN 'retrying' THEN 'r'
		           WHEN 'sent' THEN 't' WHEN 'delivered' THEN 'd' WHEN 'undelivered' THEN 'u'
		           ELSE 'f' END, '' ORDER BY id),
		       count(*) FILTER (WHERE sent_at > now() - make_interval(secs => $2)),
		       coalesce(sum(greatest(attempts - 1, 0)), 0)
		FROM deliveries WHERE campaign_id = $1 GROUP BY channel`, id, throughputWindow)
	if err != nil {
		return Progress{}, err
	}
	defer rows.Close()
	statusOf := map[rune]string{}
	for st, code := range GridCodes {
		statusOf[rune(code[0])] = st
	}
	for rows.Next() {
		var ch, grid string
		var recent, retries int
		if err := rows.Scan(&ch, &grid, &recent, &retries); err != nil {
			return Progress{}, err
		}
		p.Grid[ch] = grid
		p.Throughput[ch] = float64(recent) / throughputWindow
		p.Retries += retries
		for _, c := range grid {
			p.Counts[ch][statusOf[c]]++
		}
		p.PendingReceipts += p.Counts[ch]["sent"]
	}
	return p, rows.Err()
}

// receiptGrace is how long the stream waits for receipts after sending ends.
const receiptGrace = 2 * time.Minute

// Settled reports whether nothing more will change: sending has finished and
// every receipt is in (or stopped coming), or the campaign failed.
func (p Progress) Settled(now time.Time) bool {
	switch p.Status {
	case StatusFailed:
		return true
	case StatusCompleted:
		return p.PendingReceipts == 0 || (p.CompletedAt != nil && now.Sub(*p.CompletedAt) > receiptGrace)
	}
	return false
}

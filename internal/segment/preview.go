package segment

import (
	"context"
	"strconv"

	"campaign-composer/internal/store"
)

// Preview is what the marketer sees before approving: how many users match,
// how many of them each channel can reach (consent applied), and a few examples.
type Preview struct {
	Total          int          `json:"total"`
	SMSReachable   int          `json:"sms_reachable"`
	EmailReachable int          `json:"email_reachable"`
	Sample         []SampleUser `json:"sample"`
}

type SampleUser struct {
	ID        int64  `json:"id"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	City      string `json:"city"`
	Language  string `json:"language"`
}

const sampleSize = 5

// PreviewQuery counts and samples the users a compiled rule matches.
func PreviewQuery(ctx context.Context, db store.DBTX, q Query) (Preview, error) {
	var p Preview
	err := db.QueryRow(ctx, `
		SELECT count(*),
		       count(*) FILTER (WHERE u.sms_consent),
		       count(*) FILTER (WHERE u.email_consent)
		FROM users u WHERE `+q.Where, q.Args...).Scan(&p.Total, &p.SMSReachable, &p.EmailReachable)
	if err != nil {
		return Preview{}, store.MapErr(err)
	}

	rows, err := db.Query(ctx, `
		SELECT u.id, u.first_name, u.last_name, u.city, u.language
		FROM users u WHERE `+q.Where+` ORDER BY u.id LIMIT `+strconv.Itoa(sampleSize), q.Args...)
	if err != nil {
		return Preview{}, store.MapErr(err)
	}
	defer rows.Close()
	p.Sample = []SampleUser{}
	for rows.Next() {
		var s SampleUser
		if err := rows.Scan(&s.ID, &s.FirstName, &s.LastName, &s.City, &s.Language); err != nil {
			return Preview{}, err
		}
		p.Sample = append(p.Sample, s)
	}
	return p, store.MapErr(rows.Err())
}

package seed

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// seedLockID serialises seeding when several processes start at once.
const seedLockID = 5310002

// Users inserts generated users if the users table is empty, and returns how
// many it inserted. Running it again is a no-op, so the server can call it on
// every start.
func Users(ctx context.Context, pool *pgxpool.Pool, opt Options) (int, error) {
	if opt.N <= 0 {
		return 0, nil
	}
	inserted := 0
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", seedLockID); err != nil {
			return err
		}
		var exists bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM users)").Scan(&exists); err != nil {
			return err
		}
		if exists {
			return nil
		}
		users := Generate(opt)
		n, err := tx.CopyFrom(ctx, pgx.Identifier{"users"},
			[]string{"first_name", "last_name", "email", "phone", "city", "language",
				"sms_consent", "email_consent", "order_count", "total_spent",
				"last_purchase_at", "last_cart_at", "signed_up_at"},
			pgx.CopyFromSlice(len(users), func(i int) ([]any, error) {
				u := users[i]
				return []any{u.FirstName, u.LastName, u.Email, u.Phone, u.City, u.Language,
					u.SMSConsent, u.EmailConsent, u.OrderCount, u.TotalSpent,
					u.LastPurchaseAt, u.LastCartAt, u.SignedUpAt}, nil
			}))
		inserted = int(n)
		return err
	})
	return inserted, err
}

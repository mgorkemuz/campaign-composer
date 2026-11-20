package seed_test

import (
	"context"
	"os"
	"reflect"
	"regexp"
	"testing"
	"time"

	"campaign-composer/internal/seed"
	"campaign-composer/internal/testdb"
)

func TestMain(m *testing.M) { os.Exit(testdb.Main(m)) }

var (
	now      = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	opts     = seed.Options{N: 5000, Seed: 42, Now: now}
	phoneRe  = regexp.MustCompile(`^\+905[3-5][0-9]{8}$`)
	emailRe  = regexp.MustCompile(`^[a-z0-9.]+@example\.(com|org|net)$`)
	cityList = seed.Cities()
)

func TestGenerateIsDeterministic(t *testing.T) {
	a, b := seed.Generate(opts), seed.Generate(opts)
	if !reflect.DeepEqual(a, b) {
		t.Fatal("same options produced different users")
	}
	other := seed.Generate(seed.Options{N: 5000, Seed: 7, Now: now})
	if reflect.DeepEqual(a, other) {
		t.Fatal("a different seed produced the same users")
	}
}

func TestGeneratedUsersAreWellFormed(t *testing.T) {
	users := seed.Generate(opts)
	if len(users) != opts.N {
		t.Fatalf("got %d users", len(users))
	}
	emails := map[string]bool{}
	var badPhones, badEmails, en, sms, mail, cart int
	for _, u := range users {
		if emails[u.Email] {
			t.Fatalf("duplicate email %s", u.Email)
		}
		emails[u.Email] = true
		if !phoneRe.MatchString(u.Phone) {
			badPhones++
		}
		if !emailRe.MatchString(u.Email) {
			badEmails++
		}
		if u.Language == "en" {
			en++
		}
		if u.SMSConsent {
			sms++
		}
		if u.EmailConsent {
			mail++
		}
		if !contains(cityList, u.City) {
			t.Fatalf("unknown city %q", u.City)
		}
		if u.SignedUpAt.After(now) {
			t.Fatalf("signup in the future: %v", u.SignedUpAt)
		}
		for _, ts := range []*time.Time{u.LastPurchaseAt, u.LastCartAt} {
			if ts != nil && (ts.Before(u.SignedUpAt) || ts.After(now)) {
				t.Fatalf("activity %v outside [signup %v, now]", ts, u.SignedUpAt)
			}
		}
		if (u.OrderCount == 0) != (u.LastPurchaseAt == nil) || (u.OrderCount == 0) != (u.TotalSpent == 0) {
			t.Fatalf("orders, spend and last purchase disagree: %+v", u)
		}
		if u.LastCartAt != nil {
			cart++
		}
	}
	share := func(n int) float64 { return float64(n) / float64(len(users)) }
	checks := []struct {
		name     string
		got      float64
		low, top float64
	}{
		{"malformed phones (on purpose)", share(badPhones), 0.001, 0.015},
		{"malformed emails (on purpose)", share(badEmails), 0.001, 0.015},
		{"English speakers", share(en), 0.12, 0.18},
		{"SMS consent", share(sms), 0.66, 0.74},
		{"email consent", share(mail), 0.76, 0.84},
		{"cart activity", share(cart), 0.46, 0.54},
	}
	for _, c := range checks {
		if c.got < c.low || c.got > c.top {
			t.Errorf("%s: %.3f, want between %.3f and %.3f", c.name, c.got, c.low, c.top)
		}
	}
}

func TestUsersSeedsOnceAndSupportsTheExampleBrief(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()

	n, err := seed.Users(ctx, pool, opts)
	if err != nil || n != opts.N {
		t.Fatalf("first seed: n=%d err=%v", n, err)
	}
	n, err = seed.Users(ctx, pool, opts)
	if err != nil || n != 0 {
		t.Fatalf("second seed: n=%d err=%v; want a no-op", n, err)
	}

	// "Istanbul users who added items to their cart but didn't buy in the last
	// 30 days" must be a real, non-trivial audience.
	var audience int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM users
		WHERE city = 'İstanbul'
		  AND last_cart_at >= $1::timestamptz - interval '30 days'
		  AND (last_purchase_at IS NULL OR last_purchase_at < $1::timestamptz - interval '30 days')`,
		now).Scan(&audience); err != nil {
		t.Fatal(err)
	}
	if audience < 250 || audience > 600 {
		t.Fatalf("example audience = %d users, want a few hundred", audience)
	}
	t.Logf("example brief audience: %d of %d users", audience, opts.N)
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

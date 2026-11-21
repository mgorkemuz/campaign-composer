package webhook

import (
	"errors"
	"strconv"
	"testing"
	"time"
)

func TestSignVerify(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	body := []byte(`{"reference":"delivery-1","status":"delivered"}`)
	sig := Sign("secret", now, body)
	ts := strconv.FormatInt(now.Unix(), 10)

	if err := Verify("secret", sig, ts, body, now.Add(time.Minute)); err != nil {
		t.Fatalf("valid signature rejected: %v", err)
	}
	cases := []struct {
		name        string
		secret, sig string
		ts          string
		body        []byte
		now         time.Time
		want        error
	}{
		{"wrong secret", "other", sig, ts, body, now, ErrMismatch},
		{"tampered body", "secret", sig, ts, []byte(`{"reference":"delivery-1","status":"undelivered"}`), now, ErrMismatch},
		{"replayed with new timestamp", "secret", sig, strconv.FormatInt(now.Unix()+60, 10), body, now, ErrMismatch},
		{"too old", "secret", sig, ts, body, now.Add(6 * time.Minute), ErrStale},
		{"from the future", "secret", sig, ts, body, now.Add(-6 * time.Minute), ErrStale},
		{"missing", "secret", "", ts, body, now, ErrMissing},
		{"no prefix", "secret", sig[len("sha256="):], ts, body, now, ErrBadFormat},
		{"bad timestamp", "secret", sig, "yesterday", body, now, ErrBadFormat},
	}
	for _, c := range cases {
		if err := Verify(c.secret, c.sig, c.ts, c.body, c.now); !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", c.name, err, c.want)
		}
	}
}

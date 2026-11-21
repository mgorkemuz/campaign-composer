// Package webhook signs and verifies delivery-receipt webhooks with HMAC-SHA256.
//
// The signature covers the timestamp and the raw body ("<unix seconds>.<body>"),
// so a body cannot be replayed with a fresh timestamp, and old requests are
// refused after a tolerance window.
package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"
)

const (
	SignatureHeader = "X-Signature"
	TimestampHeader = "X-Timestamp"
	// Tolerance is how old (or how far in the future) a timestamp may be.
	Tolerance = 5 * time.Minute
)

var (
	ErrMissing   = errors.New("missing signature or timestamp")
	ErrBadFormat = errors.New("malformed signature or timestamp")
	ErrStale     = errors.New("timestamp outside the accepted window")
	ErrMismatch  = errors.New("signature does not match")
)

// Sign returns the header value for body sent at ts.
func Sign(secret string, ts time.Time, body []byte) string {
	return "sha256=" + hex.EncodeToString(mac(secret, ts.Unix(), body))
}

// Verify checks a request's signature and timestamp headers against its body.
func Verify(secret, signature, timestamp string, body []byte, now time.Time) error {
	if signature == "" || timestamp == "" {
		return ErrMissing
	}
	unix, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return ErrBadFormat
	}
	got, err := hex.DecodeString(strings.TrimPrefix(signature, "sha256="))
	if err != nil || !strings.HasPrefix(signature, "sha256=") {
		return ErrBadFormat
	}
	if d := now.Sub(time.Unix(unix, 0)); d > Tolerance || d < -Tolerance {
		return ErrStale
	}
	if !hmac.Equal(got, mac(secret, unix, body)) { // constant time
		return ErrMismatch
	}
	return nil
}

func mac(secret string, unix int64, body []byte) []byte {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(strconv.FormatInt(unix, 10)))
	h.Write([]byte("."))
	h.Write(body)
	return h.Sum(nil)
}

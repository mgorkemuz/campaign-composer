// Package backoff computes retry delays: exponential growth with jitter, capped.
// The jitter formula comes from the agents-platform LLM retry; it is shared here so
// LLM calls and message deliveries back off the same way.
package backoff

import (
	"math/rand/v2"
	"time"
)

type Policy struct {
	Base time.Duration // wait before the first retry, before jitter
	Max  time.Duration // cap for any single wait; 0 means no cap
}

// Delay returns how long to wait before retry number attempt (1 = first retry).
// The un-jittered wait doubles each time; the result is uniformly random in
// [wait/2, wait], so many failing jobs do not retry in lockstep.
func (p Policy) Delay(attempt int) time.Duration {
	wait := p.Base
	for i := 1; i < attempt && (p.Max == 0 || wait < p.Max); i++ {
		wait *= 2
	}
	if p.Max > 0 && wait > p.Max {
		wait = p.Max
	}
	if wait <= 0 {
		return 0
	}
	return wait/2 + rand.N(wait/2+1)
}

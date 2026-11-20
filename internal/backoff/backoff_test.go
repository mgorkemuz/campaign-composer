package backoff

import (
	"testing"
	"time"
)

func TestDelayGrowsWithinJitterAndCap(t *testing.T) {
	p := Policy{Base: time.Second, Max: 10 * time.Second}
	cases := []struct {
		attempt int
		full    time.Duration // un-jittered wait
	}{
		{1, 1 * time.Second},
		{2, 2 * time.Second},
		{3, 4 * time.Second},
		{4, 8 * time.Second},
		{5, 10 * time.Second}, // capped
		{50, 10 * time.Second},
	}
	for _, c := range cases {
		for range 200 {
			d := p.Delay(c.attempt)
			if d < c.full/2 || d > c.full {
				t.Fatalf("attempt %d: delay %s outside [%s, %s]", c.attempt, d, c.full/2, c.full)
			}
		}
	}
}

func TestDelayZeroBase(t *testing.T) {
	if d := (Policy{}).Delay(3); d != 0 {
		t.Fatalf("zero policy delay = %s", d)
	}
}

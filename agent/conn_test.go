package agent

import (
	"testing"
	"time"
)

// Regression test: a dial that hangs for 30 s once reset the backoff to 1 s,
// because its duration was mistaken for a healthy connection.
func TestNextAttempt(t *testing.T) {
	if got := nextAttempt(3, 0); got != 3 {
		t.Errorf("failed dial: attempt = %d, want 3 (keep growing)", got)
	}
	if got := nextAttempt(3, 5*time.Second); got != 3 {
		t.Errorf("connection that died after 5 s: attempt = %d, want 3 (keep growing)", got)
	}
	if got := nextAttempt(3, stableAfter); got != 0 {
		t.Errorf("connection that stayed up %v: attempt = %d, want 0 (start over)", stableAfter, got)
	}
}

func TestBackoffDelay(t *testing.T) {
	// base delay per attempt; the real delay is base plus up to 50% jitter
	bases := []time.Duration{1, 2, 4, 8, 16, 32, 60, 60, 60}
	for attempt, base := range bases {
		base *= time.Second
		for range 100 { // the jitter is random, so check many draws
			got := backoffDelay(attempt)
			if got < base || got >= base+base/2 {
				t.Fatalf("attempt %d: delay %v, want in [%v, %v)", attempt, got, base, base+base/2)
			}
		}
	}

	// A very high attempt count (an agent offline for days) must not overflow.
	if got := backoffDelay(1000); got < time.Minute || got >= 90*time.Second {
		t.Errorf("attempt 1000: delay %v, want in [60s, 90s)", got)
	}
}

package server

import (
	"sync"
	"testing"
	"time"
)

func TestPenalty(t *testing.T) {
	cases := []struct {
		failures int
		want     time.Duration
	}{
		{0, 0},
		{3, 0}, // the first three wrong passwords are free
		{4, time.Second},
		{5, 2 * time.Second},
		{6, 4 * time.Second},
		{13, 512 * time.Second},
		{14, maxPenalty}, // 1024 s would be more than the cap
		{1000, maxPenalty},
	}
	for _, c := range cases {
		if got := penalty(c.failures); got != c.want {
			t.Errorf("penalty(%d) = %v, want %v", c.failures, got, c.want)
		}
	}
}

func TestLoginLimiter(t *testing.T) {
	l := newLoginLimiter()
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return now }

	const ip = "203.0.113.5"

	for i := 0; i < 3; i++ {
		l.fail(ip)
	}
	if got := l.wait(ip); got != 0 {
		t.Fatalf("after 3 failures: wait = %v, want 0", got)
	}

	l.fail(ip) // the 4th
	if got := l.wait(ip); got != time.Second {
		t.Fatalf("after 4 failures: wait = %v, want 1s", got)
	}

	// Time passes, the wait counts down and then ends.
	now = now.Add(400 * time.Millisecond)
	if got := l.wait(ip); got != 600*time.Millisecond {
		t.Errorf("400ms later: wait = %v, want 600ms", got)
	}
	now = now.Add(time.Second)
	if got := l.wait(ip); got != 0 {
		t.Errorf("after the wait: wait = %v, want 0", got)
	}

	l.fail(ip) // the 5th is penalized longer
	if got := l.wait(ip); got != 2*time.Second {
		t.Errorf("after 5 failures: wait = %v, want 2s", got)
	}

	// Another address is not affected.
	if got := l.wait("198.51.100.9"); got != 0 {
		t.Errorf("unrelated address: wait = %v, want 0", got)
	}

	// A successful login clears the record.
	l.reset(ip)
	if got := l.wait(ip); got != 0 {
		t.Errorf("after reset: wait = %v, want 0", got)
	}
}

func TestLoginLimiterForgetsOldFailures(t *testing.T) {
	l := newLoginLimiter()
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return now }

	const ip = "203.0.113.5"
	for i := 0; i < 10; i++ {
		l.fail(ip)
	}
	if l.wait(ip) == 0 {
		t.Fatal("10 failures should have a wait")
	}

	now = now.Add(2 * time.Hour)
	if got := l.wait(ip); got != 0 {
		t.Errorf("2 hours later: wait = %v, want 0", got)
	}
	// And counting starts over: one failure is not enough for a delay.
	l.fail(ip)
	if got := l.wait(ip); got != 0 {
		t.Errorf("first failure after the reset: wait = %v, want 0", got)
	}
}

// TestLoginLimiterCountsAttemptsSentAtOnce is the race D-61 fixed: attempts that
// arrive together must not all pass before the first one is counted.
func TestLoginLimiterCountsAttemptsSentAtOnce(t *testing.T) {
	l := newLoginLimiter()
	const ip = "203.0.113.5"

	var wg sync.WaitGroup
	var mu sync.Mutex
	allowed := 0
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if l.begin(ip) == 0 {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	// The free ones, plus the one whose failure starts the penalty (like a 4th
	// wrong password typed by hand).
	if allowed != freeAttempts+1 {
		t.Errorf("%d of 200 attempts at once were let through, want %d", allowed, freeAttempts+1)
	}
}

func TestLoginLimiterBeginThenResetForgets(t *testing.T) {
	l := newLoginLimiter()
	const ip = "203.0.113.5"
	for i := 0; i < freeAttempts; i++ {
		l.begin(ip)
	}
	l.reset(ip) // the last one was the right password
	if got := l.begin(ip); got != 0 {
		t.Errorf("after a successful login: wait = %v, want 0", got)
	}
}

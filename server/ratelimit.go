package server

import (
	"sync"
	"time"
)

const (
	freeAttempts = 3                // wrong passwords allowed before any delay
	maxPenalty   = 15 * time.Minute // the delay never grows beyond this
	forgetAfter  = time.Hour        // an address with no failures for this long starts fresh
	maxTracked   = 1000             // when the table is this big, drop entries that expired
)

// loginLimiter slows down password guessing, per client address
// (architecture.md §7). It lives in memory: a restart forgets everything,
// which is fine for something this simple.
type loginLimiter struct {
	mu   sync.Mutex
	byIP map[string]*failures
	now  func() time.Time // a field so tests can move the clock instead of sleeping
}

type failures struct {
	count int
	last  time.Time
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{byIP: make(map[string]*failures), now: time.Now}
}

// penalty is how long to wait after the count-th wrong password in a row:
// nothing for the first 3, then 1 s, 2 s, 4 s, ... up to 15 minutes. A person
// who mistypes twice never notices it. A script guessing passwords ends up
// with about 15 guesses per hour per address.
func penalty(count int) time.Duration {
	if count <= freeAttempts {
		return 0
	}
	shift := count - freeAttempts - 1
	if shift >= 10 { // 1<<10 s is already above the cap, and a bigger shift could overflow
		return maxPenalty
	}
	return min(time.Second<<shift, maxPenalty)
}

// wait returns how long this address must still wait before its next login
// attempt, or 0 if it may try now.
func (l *loginLimiter) wait(ip string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()

	f, ok := l.byIP[ip]
	if !ok {
		return 0
	}
	now := l.now()
	if now.Sub(f.last) > forgetAfter {
		delete(l.byIP, ip)
		return 0
	}
	return max(0, f.last.Add(penalty(f.count)).Sub(now))
}

// fail records one wrong password.
func (l *loginLimiter) fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	f := l.byIP[ip]
	if f == nil || now.Sub(f.last) > forgetAfter {
		f = &failures{}
		l.byIP[ip] = f
	}
	f.count++
	f.last = now

	// Without this, someone using many addresses could grow the table forever.
	if len(l.byIP) > maxTracked {
		for addr, entry := range l.byIP {
			if now.Sub(entry.last) > forgetAfter {
				delete(l.byIP, addr)
			}
		}
	}
}

// reset forgets an address after a successful login.
func (l *loginLimiter) reset(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.byIP, ip)
}

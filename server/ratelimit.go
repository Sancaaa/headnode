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
	return l.waitLocked(ip)
}

// begin is wait and fail in one step, under one lock: if the address may try now,
// the attempt is counted as a failure at once, before the password is checked, and
// a successful login undoes it with reset.
//
// Checking first and counting after the password check (about 50 ms of bcrypt)
// would let many attempts sent at the same moment all pass the check before the
// first one is counted: 500 at once would be 500 guesses, not 4.
func (l *loginLimiter) begin(ip string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	if wait := l.waitLocked(ip); wait > 0 {
		return wait
	}
	l.failLocked(ip)
	return 0
}

func (l *loginLimiter) waitLocked(ip string) time.Duration {
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
	l.failLocked(ip)
}

func (l *loginLimiter) failLocked(ip string) {
	now := l.now()
	f := l.byIP[ip]
	if f == nil || now.Sub(f.last) > forgetAfter {
		f = &failures{}
		l.byIP[ip] = f
	}
	f.count++
	f.last = now

	// Without a hard cap, a flood from more than maxTracked distinct addresses
	// within forgetAfter would grow byIP without bound. Prune expired entries
	// first, then evict the oldest remaining entries until we are back at cap.
	if len(l.byIP) > maxTracked {
		for addr, entry := range l.byIP {
			if now.Sub(entry.last) > forgetAfter {
				delete(l.byIP, addr)
			}
		}
		for len(l.byIP) > maxTracked {
			var oldestKey string
			var oldestTime time.Time
			for addr, entry := range l.byIP {
				if addr == ip {
					continue
				}
				if oldestKey == "" || entry.last.Before(oldestTime) {
					oldestKey = addr
					oldestTime = entry.last
				}
			}
			if oldestKey == "" {
				break
			}
			delete(l.byIP, oldestKey)
		}
	}
}

// reset forgets an address after a successful login.
func (l *loginLimiter) reset(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.byIP, ip)
}

// undo takes back one attempt counted by begin when the step was not a failure
// (such as the password-only first step of a TOTP login). Unlike reset, it
// preserves any earlier failed attempts so repeating step 1 cannot clear them.
func (l *loginLimiter) undo(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	f := l.byIP[ip]
	if f == nil {
		return
	}
	f.count--
	if f.count <= 0 {
		delete(l.byIP, ip)
	}
}

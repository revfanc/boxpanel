package auth

import (
	"sync"
	"time"
)

const lockDuration = 15 * time.Minute

type attempts struct {
	failures int
	until    time.Time
}

// Limiter counts consecutive failures per peer IP. Entries expire after 15
// minutes of inactivity; the fifth failure starts a fresh 15-minute lock.
type Limiter struct {
	mu      sync.Mutex
	entries map[string]attempts
	now     func() time.Time
}

func NewLimiter(now func() time.Time) *Limiter {
	return &Limiter{entries: make(map[string]attempts), now: now}
}

func (l *Limiter) RetryAfter(ip string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	// Bound memory without background goroutines or evicting active lockouts.
	for key, entry := range l.entries {
		if !now.Before(entry.until) {
			delete(l.entries, key)
		}
	}
	entry := l.entries[ip]
	if entry.failures >= 5 {
		return entry.until.Sub(now)
	}
	if _, exists := l.entries[ip]; !exists && len(l.entries) >= 10000 {
		return time.Minute
	}
	return 0
}

func (l *Limiter) Failure(ip string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	entry := l.entries[ip]
	if !now.Before(entry.until) {
		entry = attempts{}
	}
	entry.failures++
	entry.until = now.Add(lockDuration)
	l.entries[ip] = entry
	if entry.failures >= 5 {
		return lockDuration
	}
	return 0
}

func (l *Limiter) Success(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, ip)
}

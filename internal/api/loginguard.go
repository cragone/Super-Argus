package api

import (
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// loginGuard slows password guessing. Failures are counted per email and per
// client IP; past a threshold the key is locked for an exponentially growing
// time (30s, 1m, 2m ... 15m). A locked key is refused even with the right
// password, so a guesser learns nothing from the lock ending. Counts decay
// after 15 quiet minutes. State is in memory, so a restart clears it.
type loginGuard struct {
	mu   sync.Mutex
	m    map[string]*attempt
	now  func() time.Time
	base time.Duration
	max  time.Duration
	// maxKeys bounds memory; when full, idle entries are swept and, if none
	// can be dropped, new keys go untracked rather than growing without limit.
	maxKeys int
}

type attempt struct {
	fails       int
	last        time.Time
	lockedUntil time.Time
}

const (
	emailFailLimit = 5
	ipFailLimit    = 20
	guardDecay     = 15 * time.Minute
)

func newLoginGuard() *loginGuard {
	return &loginGuard{m: map[string]*attempt{}, now: time.Now, base: 30 * time.Second, max: 15 * time.Minute, maxKeys: 50000}
}

// retryAfter returns how long the longest lock among keys has left (0 if none).
func (g *loginGuard) retryAfter(keys ...string) time.Duration {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	var wait time.Duration
	for _, k := range keys {
		if a := g.m[k]; a != nil && a.lockedUntil.After(now) {
			wait = max(wait, a.lockedUntil.Sub(now))
		}
	}
	return wait
}

// fail records a failed attempt on key, locking it once limit is reached.
func (g *loginGuard) fail(key string, limit int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	a := g.m[key]
	if a == nil {
		if len(g.m) >= g.maxKeys {
			g.sweepLocked(now)
			if len(g.m) >= g.maxKeys {
				return
			}
		}
		a = &attempt{}
		g.m[key] = a
	}
	if now.Sub(a.last) > guardDecay && !a.lockedUntil.After(now) {
		a.fails = 0
	}
	a.fails++
	a.last = now
	if a.fails >= limit {
		d := g.base
		for i := limit; i < a.fails && d < g.max; i++ {
			d *= 2
		}
		a.lockedUntil = now.Add(min(d, g.max))
	}
}

// succeed clears the counter for key.
func (g *loginGuard) succeed(key string) {
	g.mu.Lock()
	delete(g.m, key)
	g.mu.Unlock()
}

func (g *loginGuard) sweepLocked(now time.Time) {
	for k, a := range g.m {
		if now.Sub(a.last) > guardDecay && !a.lockedUntil.After(now) {
			delete(g.m, k)
		}
	}
}

// clientIP is the TCP peer address. Forwarding headers are deliberately not
// trusted: behind a reverse proxy every client then shares one IP key, which
// is why the per-email limit is the primary control.
func clientIP(r *http.Request) string {
	if h, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return h
	}
	return r.RemoteAddr
}

func retrySeconds(d time.Duration) string {
	return strconv.Itoa(int((d + time.Second - 1) / time.Second))
}

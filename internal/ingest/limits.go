package ingest

import (
	"sync"
	"time"
)

// Limits are enforced at ingest.
type Limits struct {
	MaxLabels          int
	MaxLabelValueBytes int
	MaxLineBytes       int
	MaxStreamsPerKey   int
	MaxBodyBytes       int64
	MaxAge             time.Duration // reject timestamps older than this
	MaxFuture          time.Duration // reject timestamps further ahead than this
	DefaultLinesPerSec int           // per-key rate when the key has no RateLimit
	BytesPerSec        int           // per-key byte rate
	BurstFactor        float64       // bucket capacity as a multiple of the per-second rate
}

// DefaultLimits returns the limits from the build plan.
func DefaultLimits() Limits {
	return Limits{
		MaxLabels: 15, MaxLabelValueBytes: 256, MaxLineBytes: 64 << 10, MaxStreamsPerKey: 10000,
		MaxBodyBytes: 5 << 20, MaxAge: 24 * time.Hour, MaxFuture: 5 * time.Minute,
		DefaultLinesPerSec: 10000, BytesPerSec: 10 << 20, BurstFactor: 5,
	}
}

type bucket struct {
	tokens float64
	last   time.Time
}

func (b *bucket) refill(now time.Time, rate, capacity float64) {
	if b.last.IsZero() {
		b.tokens = capacity
	} else if dt := now.Sub(b.last).Seconds(); dt > 0 {
		b.tokens = min(capacity, b.tokens+dt*rate)
	}
	b.last = now
}

// limiter is a per-key token-bucket limiter for lines/sec and bytes/sec.
type limiter struct {
	mu    sync.Mutex
	lines map[string]*bucket
	bytes map[string]*bucket
}

func newLimiter() *limiter {
	return &limiter{lines: map[string]*bucket{}, bytes: map[string]*bucket{}}
}

type verdict int

const (
	allowed verdict = iota
	throttled
	tooLarge // can never fit in the bucket
)

// take charges lines and bytes against key atomically: either both buckets
// have room and are charged, or neither is.
func (l *limiter) take(key string, lines, bytes int, linesPerSec, bytesPerSec int, burst float64, now time.Time) verdict {
	if linesPerSec <= 0 && bytesPerSec <= 0 {
		return allowed
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	get := func(m map[string]*bucket) *bucket {
		b := m[key]
		if b == nil {
			b = &bucket{}
			m[key] = b
		}
		return b
	}
	lb, bb := get(l.lines), get(l.bytes)
	lcap, bcap := float64(linesPerSec)*burst, float64(bytesPerSec)*burst
	if linesPerSec > 0 {
		lb.refill(now, float64(linesPerSec), lcap)
		if float64(lines) > lcap {
			return tooLarge
		}
	}
	if bytesPerSec > 0 {
		bb.refill(now, float64(bytesPerSec), bcap)
		if float64(bytes) > bcap {
			return tooLarge
		}
	}
	if (linesPerSec > 0 && float64(lines) > lb.tokens) || (bytesPerSec > 0 && float64(bytes) > bb.tokens) {
		return throttled
	}
	if linesPerSec > 0 {
		lb.tokens -= float64(lines)
	}
	if bytesPerSec > 0 {
		bb.tokens -= float64(bytes)
	}
	return allowed
}

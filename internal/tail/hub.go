// Package tail is the pub/sub hub behind live tail.
//
// Ingest publishes every accepted batch; each tail connection owns a bounded
// buffer. Publishing never blocks: when a subscriber's buffer is full the batch
// is dropped for that subscriber only and counted, so a stalled client can
// never slow ingest down. Filtering happens on the subscriber's side, so the
// ingest path costs one non-blocking channel send per connected tail.
package tail

import (
	"errors"
	"sync"
	"sync/atomic"

	"github.com/cragone/Super-Argus/internal/wal"
)

// ErrTooManyTails is returned when a user (or the server) has too many open tails.
var ErrTooManyTails = errors.New("too many live tail connections")

// Config bounds the hub.
type Config struct {
	PerUser int // open tails per user (0 = unlimited)
	Max     int // open tails overall (0 = unlimited)
	Buffer  int // batches buffered per tail before dropping
}

// DefaultConfig returns the production defaults.
func DefaultConfig() Config { return Config{PerUser: 5, Max: 500, Buffer: 256} }

// Hub fans published batches out to subscribers.
type Hub struct {
	cfg Config

	mu      sync.RWMutex
	subs    map[*Sub]struct{}
	perUser map[string]int
}

// New returns a hub.
func New(cfg Config) *Hub {
	if cfg.Buffer <= 0 {
		cfg.Buffer = 256
	}
	return &Hub{cfg: cfg, subs: map[*Sub]struct{}{}, perUser: map[string]int{}}
}

// Sub is one tail connection's feed.
type Sub struct {
	hub     *Hub
	user    string
	ch      chan []wal.Record
	dropped atomic.Int64
	closed  atomic.Bool
}

// Subscribe registers a tail for user, enforcing the caps.
func (h *Hub) Subscribe(user string) (*Sub, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if (h.cfg.PerUser > 0 && h.perUser[user] >= h.cfg.PerUser) || (h.cfg.Max > 0 && len(h.subs) >= h.cfg.Max) {
		return nil, ErrTooManyTails
	}
	s := &Sub{hub: h, user: user, ch: make(chan []wal.Record, h.cfg.Buffer)}
	h.subs[s] = struct{}{}
	h.perUser[user]++
	return s, nil
}

// Publish offers recs to every subscriber without blocking. The slice is shared
// and must not be modified afterwards.
func (h *Hub) Publish(recs []wal.Record) {
	if len(recs) == 0 {
		return
	}
	h.mu.RLock()
	for s := range h.subs {
		select {
		case s.ch <- recs:
		default:
			s.dropped.Add(int64(len(recs)))
		}
	}
	h.mu.RUnlock()
}

// Active returns the number of open tails.
func (h *Hub) Active() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.subs)
}

// C delivers published batches.
func (s *Sub) C() <-chan []wal.Record { return s.ch }

// TakeDropped returns and resets the number of lines dropped because the
// subscriber was too slow.
func (s *Sub) TakeDropped() int64 { return s.dropped.Swap(0) }

// Close unregisters the subscriber. It is safe to call more than once.
func (s *Sub) Close() {
	if !s.closed.CompareAndSwap(false, true) {
		return
	}
	h := s.hub
	h.mu.Lock()
	delete(h.subs, s)
	if h.perUser[s.user]--; h.perUser[s.user] <= 0 {
		delete(h.perUser, s.user)
	}
	h.mu.Unlock()
}

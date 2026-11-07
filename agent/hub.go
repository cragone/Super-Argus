package main

import (
	"context"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cragone/Super-Argus/sdk/go/argus"
)

// maxShippers bounds memory if something produces unbounded distinct label sets.
const maxShippers = 500

// Hub fans lines out to one SDK shipper per distinct label set (a shipper
// carries a single stream's labels).
type Hub struct {
	url, key string
	extra    map[string]string // added to every stream, e.g. env, cluster

	mu      sync.Mutex
	ships   map[string]*argus.Shipper
	dropped int64
}

func NewHub(url, key string, extra map[string]string) *Hub {
	return &Hub{url: url, key: key, extra: extra, ships: map[string]*argus.Shipper{}}
}

func labelKey(l map[string]string) string {
	keys := make([]string, 0, len(l))
	for k := range l {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(l[k])
		b.WriteByte(',')
	}
	return b.String()
}

// Send ships one line under the given labels. It never blocks.
func (h *Hub) Send(labels map[string]string, ts time.Time, line string) {
	all := make(map[string]string, len(labels)+len(h.extra))
	for k, v := range h.extra {
		all[k] = v
	}
	for k, v := range labels {
		all[k] = v
	}
	key := labelKey(all)

	h.mu.Lock()
	s := h.ships[key]
	if s == nil {
		if len(h.ships) >= maxShippers {
			h.dropped++
			if h.dropped == 1 || h.dropped%10000 == 0 {
				log.Printf("argus-agent: %d label sets in use, dropping new ones (%d lines dropped)", maxShippers, h.dropped)
			}
			h.mu.Unlock()
			return
		}
		s = argus.New(argus.Config{URL: h.url, APIKey: h.key, Labels: all, QueueSize: 2000})
		h.ships[key] = s
	}
	h.mu.Unlock()
	s.Send(ts, line)
}

// Close flushes every shipper, waiting up to ctx.
func (h *Hub) Close(ctx context.Context) {
	h.mu.Lock()
	ships := make([]*argus.Shipper, 0, len(h.ships))
	for _, s := range h.ships {
		ships = append(ships, s)
	}
	h.mu.Unlock()
	var wg sync.WaitGroup
	for _, s := range ships {
		wg.Add(1)
		go func(s *argus.Shipper) { defer wg.Done(); s.Close(ctx) }(s)
	}
	wg.Wait()
}

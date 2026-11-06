package main

import (
	"io/fs"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/cragone/Super-Argus/internal/ingest"
	"github.com/cragone/Super-Argus/internal/metrics"
	"github.com/cragone/Super-Argus/internal/tail"
	"github.com/cragone/Super-Argus/internal/wal"
)

// dirSize returns a function giving the total size of files under root,
// recomputed at most once per ttl so frequent scrapes do not walk the disk.
func dirSize(root string, ttl time.Duration) func() float64 {
	var (
		mu   sync.Mutex
		at   time.Time
		size float64
	)
	return func() float64 {
		mu.Lock()
		defer mu.Unlock()
		if !at.IsZero() && time.Since(at) < ttl {
			return size
		}
		var n int64
		_ = filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil // files come and go under us; skip
			}
			if !d.IsDir() {
				if fi, err := d.Info(); err == nil {
					n += fi.Size()
				}
			}
			return nil
		})
		size, at = float64(n), time.Now()
		return size
	}
}

// registerGauges adds the scrape-time gauges for the running server.
func registerGauges(r *metrics.Registry, dataDir string, eng *ingest.Engine, w *wal.WAL, hub *tail.Hub) {
	start := time.Now()
	r.Gauge("argus_head_lines", "Lines held in head chunks, not yet flushed to chunk files.", func() float64 { return float64(eng.HeadLines()) })
	r.Gauge("argus_wal_segments", "WAL segment files on disk.", func() float64 { n, _ := w.DiskUsage(); return float64(n) })
	r.Gauge("argus_wal_bytes", "WAL bytes on disk (replay backlog if the process died now).", func() float64 { _, b := w.DiskUsage(); return float64(b) })
	r.Gauge("argus_tail_active_streams", "Open live-tail connections.", func() float64 { return float64(hub.Active()) })
	r.Gauge("argus_data_dir_bytes", "Total size of the data directory (cached for 30s).", dirSize(dataDir, 30*time.Second))
	r.Gauge("argus_uptime_seconds", "Seconds since the server started.", func() float64 { return time.Since(start).Seconds() })
	r.Gauge("go_goroutines", "Number of goroutines.", func() float64 { return float64(runtime.NumGoroutine()) })
	r.Gauge("go_heap_alloc_bytes", "Bytes of allocated heap objects.", func() float64 {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		return float64(m.HeapAlloc)
	})
}

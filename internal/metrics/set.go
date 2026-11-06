package metrics

import "time"

// Set is the application's instruments. All methods are safe on a nil *Set so
// components can run without metrics (tests, tools).
type Set struct {
	Registry *Registry

	linesAccepted *Counter
	bytesAccepted *Counter
	linesRejected *Counter
	linesSampled  *Counter
	rateLimited   *Counter
	chunksFlushed *Counter
	flushErrors   *Counter
	flushDuration *Histogram
	queryDur      map[string]*Histogram
	queryErr      map[string]*Counter
}

// QueryKinds are the values accepted by Query.
var QueryKinds = []string{"log", "metric"}

// NewSet registers the standard instruments on r.
func NewSet(r *Registry) *Set {
	s := &Set{Registry: r, queryDur: map[string]*Histogram{}, queryErr: map[string]*Counter{}}
	s.linesAccepted = r.Counter("argus_ingest_lines_total", "Lines accepted by the push endpoint.")
	s.bytesAccepted = r.Counter("argus_ingest_bytes_total", "Raw line bytes accepted by the push endpoint.")
	s.linesRejected = r.Counter("argus_ingest_rejected_lines_total", "Lines rejected by validation or stream limits.")
	s.linesSampled = r.Counter("argus_ingest_sampled_lines_total", "Lines dropped by the sampling policy.")
	s.rateLimited = r.Counter("argus_ingest_rate_limited_requests_total", "Push requests answered 429.")
	s.chunksFlushed = r.Counter("argus_chunks_flushed_total", "Head chunks written to the chunk store.")
	s.flushErrors = r.Counter("argus_flush_errors_total", "Flush passes that failed.")
	s.flushDuration = r.Histogram("argus_flush_duration_seconds", "Time of a flush pass that wrote chunks.", nil)
	for _, k := range QueryKinds {
		s.queryDur[k] = r.Histogram("argus_query_duration_seconds", "Query latency.", nil, "kind", k)
		s.queryErr[k] = r.Counter("argus_query_errors_total", "Queries that returned an error.", "kind", k)
	}
	return s
}

// Accepted records lines stored by a push.
func (s *Set) Accepted(lines, bytes int) {
	if s == nil {
		return
	}
	s.linesAccepted.Add(lines)
	s.bytesAccepted.Add(bytes)
}

// Rejected records lines refused by validation or limits.
func (s *Set) Rejected(n int) {
	if s != nil {
		s.linesRejected.Add(n)
	}
}

// Sampled records lines dropped by sampling.
func (s *Set) Sampled(n int) {
	if s != nil {
		s.linesSampled.Add(n)
	}
}

// RateLimited records a throttled push request.
func (s *Set) RateLimited() {
	if s != nil {
		s.rateLimited.Inc()
	}
}

// Flush records a flush pass that wrote chunks (or failed).
func (s *Set) Flush(chunks int, d time.Duration, err error) {
	if s == nil {
		return
	}
	s.chunksFlushed.Add(chunks)
	if err != nil {
		s.flushErrors.Inc()
	}
	s.flushDuration.Observe(d.Seconds())
}

// Query records one query of the given kind ("log" or "metric").
func (s *Set) Query(kind string, d time.Duration, err error) {
	if s == nil {
		return
	}
	if h := s.queryDur[kind]; h != nil {
		h.Observe(d.Seconds())
	}
	if err != nil {
		if c := s.queryErr[kind]; c != nil {
			c.Inc()
		}
	}
}

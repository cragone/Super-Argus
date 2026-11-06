package query

import (
	"container/heap"
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cragone/Super-Argus/internal/chunk"
	"github.com/cragone/Super-Argus/internal/models"
	"gorm.io/gorm"
)

// Limits bound what a single query may cost.
type Limits struct {
	MaxRange           time.Duration // widest allowed [start, end)
	DefaultLimit       int           // lines returned when the request gives no limit
	MaxLimit           int           // hard cap on lines returned
	MaxBytesScanned    int64         // raw bytes a query may scan before it is aborted
	Timeout            time.Duration // per-query deadline
	PerUserConcurrency int           // simultaneous queries per user
	Workers            int           // parallel chunk scanners (0 = NumCPU)
	MaxMetricRange     time.Duration // widest range for metric queries
	MaxSteps           int           // points per series
	MaxSeries          int           // series per metric query
}

// DefaultLimits returns the limits from the build plan.
func DefaultLimits() Limits {
	return Limits{MaxRange: 7 * 24 * time.Hour, DefaultLimit: 1000, MaxLimit: 5000,
		MaxBytesScanned: 4 << 30, Timeout: 30 * time.Second, PerUserConcurrency: 4,
		MaxMetricRange: 31 * 24 * time.Hour, MaxSteps: 2000, MaxSeries: 500}
}

// Error kinds, wrapped by *Error so the API layer can pick a status code.
var (
	ErrBadRequest = errors.New("bad request")
	ErrLimit      = errors.New("query limit exceeded")
	ErrBusy       = errors.New("too many concurrent queries")
	ErrTimeout    = errors.New("query timed out")
)

// Error is a typed query failure.
type Error struct {
	Kind error
	Msg  string
}

func (e *Error) Error() string { return e.Msg }
func (e *Error) Unwrap() error { return e.Kind }

func badf(format string, a ...any) error { return &Error{ErrBadRequest, fmt.Sprintf(format, a...)} }

// Service answers queries against the index, chunk store and live heads.
type Service struct {
	DB     *gorm.DB
	Store  chunk.Store
	Heads  chunk.HeadSource // may be nil
	Limits Limits
	Now    func() time.Time

	mu     sync.Mutex
	active map[string]int // user -> running queries
	mcache *metricCache
}

// NewService builds a query service.
func NewService(db *gorm.DB, store chunk.Store, heads chunk.HeadSource, l Limits) *Service {
	return &Service{DB: db, Store: store, Heads: heads, Limits: l, Now: time.Now, active: map[string]int{}, mcache: newMetricCache(256)}
}

// Request is a range query. Start is inclusive, End exclusive, in unix nanoseconds.
type Request struct {
	Query    string
	Start    int64
	End      int64
	Limit    int
	Backward bool // newest first (default for the API)
}

// Entry is one result line.
type Entry struct {
	TS     int64             `json:"-"`
	Stream uint64            `json:"-"`
	Line   string            `json:"line"`
	Fields map[string]string `json:"fields,omitempty"`
}

// Stats describes the work a query did.
type Stats struct {
	LinesScanned  int64 `json:"lines_scanned"`
	BytesScanned  int64 `json:"bytes_scanned"`
	ChunksScanned int   `json:"chunks_scanned"`
	ChunksSkipped int   `json:"chunks_skipped"`
	HeadLines     int   `json:"head_lines"`
	Streams       int   `json:"streams"`
	ElapsedMS     int64 `json:"elapsed_ms"`
}

// Result is the answer to a range query.
type Result struct {
	Entries []Entry
	Streams map[uint64]map[string]string
	Stats   Stats
}

func (s *Service) acquire(user string) (release func(), err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Limits.PerUserConcurrency > 0 && s.active[user] >= s.Limits.PerUserConcurrency {
		return nil, &Error{ErrBusy, fmt.Sprintf("at most %d concurrent queries per user", s.Limits.PerUserConcurrency)}
	}
	s.active[user]++
	return func() {
		s.mu.Lock()
		if s.active[user]--; s.active[user] <= 0 {
			delete(s.active, user)
		}
		s.mu.Unlock()
	}, nil
}

func (s *Service) validate(req *Request) error {
	if req.Start <= 0 || req.End <= 0 {
		return badf("start and end are required")
	}
	if req.End <= req.Start {
		return badf("end must be after start")
	}
	if s.Limits.MaxRange > 0 && time.Duration(req.End-req.Start) > s.Limits.MaxRange {
		return &Error{ErrLimit, fmt.Sprintf("time range exceeds the maximum of %s", s.Limits.MaxRange)}
	}
	if req.Limit <= 0 {
		req.Limit = s.Limits.DefaultLimit
	}
	if s.Limits.MaxLimit > 0 && req.Limit > s.Limits.MaxLimit {
		req.Limit = s.Limits.MaxLimit
	}
	return nil
}

// Range runs a log query and returns up to req.Limit entries ordered by time.
func (s *Service) Range(ctx context.Context, user string, req Request) (*Result, error) {
	began := time.Now()
	if err := s.validate(&req); err != nil {
		return nil, err
	}
	q, err := Parse(req.Query)
	if err != nil {
		return nil, &Error{ErrBadRequest, err.Error()}
	}
	pipe, err := Compile(q)
	if err != nil {
		return nil, &Error{ErrBadRequest, err.Error()}
	}
	release, err := s.acquire(user)
	if err != nil {
		return nil, err
	}
	defer release()
	if s.Limits.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.Limits.Timeout)
		defer cancel()
	}

	streams, err := s.resolveStreams(ctx, q, pipe)
	if err != nil {
		return nil, wrapCtx(ctx, err)
	}
	res := &Result{Streams: map[uint64]map[string]string{}}
	res.Stats.Streams = len(streams)
	if len(streams) == 0 {
		res.Stats.ElapsedMS = time.Since(began).Milliseconds()
		return res, nil
	}
	ids := make(map[uint64]struct{}, len(streams))
	idList := make([]uint64, 0, len(streams))
	for id := range streams {
		ids[id] = struct{}{}
		idList = append(idList, id)
	}

	// Order matters: snapshot the heads first, then list chunks. A head that
	// was flushed in between shows up in both; it is dropped by chunk ID below.
	var heads []chunk.HeadSnapshot
	if s.Heads != nil {
		heads = s.Heads.SnapshotHeads(ids, req.Start, req.End)
	}
	chunks, err := s.listChunks(ctx, idList, req)
	if err != nil {
		return nil, wrapCtx(ctx, err)
	}

	col := newTopN(req.Limit, req.Backward)
	shared := &sharedTopN{topN: col}
	var st statsAcc
	scan := &scanner{pipe: pipe, streams: streams, st: &st, maxBytes: s.Limits.MaxBytesScanned}

	listed := make(map[string]struct{}, len(chunks))
	for _, c := range chunks {
		listed[c.ID] = struct{}{}
	}
	for _, h := range heads {
		if _, dup := listed[h.ChunkID]; dup {
			continue
		}
		local := newTopN(req.Limit, req.Backward)
		for _, e := range h.Entries {
			if err := scan.line(ctx, local, h.StreamID, e); err != nil {
				return nil, wrapCtx(ctx, err)
			}
			st.headLines.Add(1)
		}
		shared.merge(local)
	}

	if req.Backward {
		sort.Slice(chunks, func(i, j int) bool { return chunks[i].MaxTS > chunks[j].MaxTS })
	} else {
		sort.Slice(chunks, func(i, j int) bool { return chunks[i].MinTS < chunks[j].MinTS })
	}
	if err := s.scanChunks(ctx, chunks, req, scan, shared); err != nil {
		return nil, wrapCtx(ctx, err)
	}

	res.Entries = col.sorted()
	for _, e := range res.Entries {
		if _, ok := res.Streams[e.Stream]; !ok {
			res.Streams[e.Stream] = streams[e.Stream]
		}
	}
	res.Stats = st.snapshot(len(streams), time.Since(began))
	return res, nil
}

func wrapCtx(ctx context.Context, err error) error {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return &Error{ErrTimeout, "query timed out; narrow the time range or add stricter matchers"}
	}
	return err
}

// scanChunks reads chunks with a worker pool, stopping early once the result
// is full and the remaining chunks (ordered by direction) cannot improve it.
func (s *Service) scanChunks(ctx context.Context, chunks []models.Chunk, req Request, scan *scanner, shared *sharedTopN) error {
	workers := s.Limits.Workers
	if workers <= 0 {
		workers = runtime.NumCPU()
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	work := make(chan models.Chunk)
	var wg sync.WaitGroup
	var firstErr error
	var errOnce sync.Once
	fail := func(err error) { errOnce.Do(func() { firstErr = err; cancel() }) }

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for c := range work {
				if ctx.Err() != nil {
					continue // drain
				}
				if shared.cannotImprove(c, req.Backward) {
					scan.st.chunksSkipped.Add(1)
					continue
				}
				local := newTopN(req.Limit, req.Backward)
				if err := s.scanChunk(ctx, c, req, scan, local); err != nil {
					fail(err)
					continue
				}
				scan.st.chunksScanned.Add(1)
				shared.merge(local)
			}
		}()
	}
feed:
	for _, c := range chunks {
		select {
		case work <- c:
		case <-ctx.Done():
			break feed
		}
	}
	close(work)
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}
	return ctx.Err()
}

func (s *Service) scanChunk(ctx context.Context, c models.Chunk, req Request, scan *scanner, local *topN) error {
	data, err := s.Store.Get(c.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil // deleted by retention between listing and reading
	}
	if err != nil {
		return err
	}
	r, err := chunk.NewReader(data)
	if err != nil {
		return fmt.Errorf("chunk %s: %w", c.ID, err)
	}
	var scanErr error
	err = r.ScanContaining(req.Start, req.End, scan.pipe.Needles(), func(e chunk.Entry) bool {
		if scanErr = scan.line(ctx, local, c.StreamID, e); scanErr != nil {
			return false
		}
		return true
	})
	if scanErr != nil {
		return scanErr
	}
	if err != nil {
		return fmt.Errorf("chunk %s: %w", c.ID, err)
	}
	return nil
}

// scanner applies the pipeline to lines and enforces the scan budget.
type scanner struct {
	pipe     *Pipeline
	streams  map[uint64]map[string]string
	st       *statsAcc
	maxBytes int64
}

func (sc *scanner) line(ctx context.Context, out *topN, stream uint64, e chunk.Entry) error {
	n := sc.st.lines.Add(1)
	b := sc.st.bytes.Add(int64(len(e.Line)))
	if n&1023 == 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	if sc.maxBytes > 0 && b > sc.maxBytes {
		return &Error{ErrLimit, fmt.Sprintf("query scanned more than %d bytes; narrow the time range or add stricter matchers", sc.maxBytes)}
	}
	fields, ok := sc.pipe.Process(sc.streams[stream], e.Line)
	if !ok {
		return nil
	}
	out.add(Entry{TS: e.TS, Stream: stream, Line: e.Line, Fields: fields})
	return nil
}

type statsAcc struct {
	lines, bytes, headLines      atomic.Int64
	chunksScanned, chunksSkipped atomic.Int64
}

func (a *statsAcc) snapshot(streams int, d time.Duration) Stats {
	return Stats{LinesScanned: a.lines.Load(), BytesScanned: a.bytes.Load(), ChunksScanned: int(a.chunksScanned.Load()),
		ChunksSkipped: int(a.chunksSkipped.Load()), HeadLines: int(a.headLines.Load()), Streams: streams, ElapsedMS: d.Milliseconds()}
}

// --- bounded result collection ---

// topN keeps the best `limit` entries: the newest when backward, else the oldest.
type topN struct {
	limit    int
	backward bool
	h        entryHeap
}

func newTopN(limit int, backward bool) *topN {
	return &topN{limit: limit, backward: backward, h: entryHeap{backward: backward}}
}

// entryHeap's root is the worst kept entry (oldest when backward, newest otherwise).
type entryHeap struct {
	backward bool
	es       []Entry
}

func (h entryHeap) Len() int { return len(h.es) }
func (h entryHeap) Less(i, j int) bool {
	if h.backward {
		return before(h.es[i], h.es[j])
	}
	return before(h.es[j], h.es[i])
}

// before is a total order (time, stream, line) so results are deterministic
// even when many lines share a timestamp.
func before(a, b Entry) bool {
	if a.TS != b.TS {
		return a.TS < b.TS
	}
	if a.Stream != b.Stream {
		return a.Stream < b.Stream
	}
	return a.Line < b.Line
}
func (h entryHeap) Swap(i, j int) { h.es[i], h.es[j] = h.es[j], h.es[i] }
func (h *entryHeap) Push(x any)   { h.es = append(h.es, x.(Entry)) }
func (h *entryHeap) Pop() any {
	n := len(h.es)
	x := h.es[n-1]
	h.es = h.es[:n-1]
	return x
}

func (t *topN) add(e Entry) {
	if len(t.h.es) < t.limit {
		heap.Push(&t.h, e)
		return
	}
	w := t.h.es[0]
	if (t.backward && before(w, e)) || (!t.backward && before(e, w)) {
		t.h.es[0] = e
		heap.Fix(&t.h, 0)
	}
}

func (t *topN) full() bool { return len(t.h.es) >= t.limit }

// sorted returns the kept entries in output order.
func (t *topN) sorted() []Entry {
	out := append([]Entry(nil), t.h.es...)
	sort.SliceStable(out, func(i, j int) bool {
		if t.backward {
			return before(out[j], out[i])
		}
		return before(out[i], out[j])
	})
	return out
}

type sharedTopN struct {
	mu sync.Mutex
	*topN
}

func (s *sharedTopN) merge(local *topN) {
	if len(local.h.es) == 0 {
		return
	}
	s.mu.Lock()
	for _, e := range local.h.es {
		s.topN.add(e)
	}
	s.mu.Unlock()
}

// cannotImprove reports whether c holds nothing that could displace a kept entry.
func (s *sharedTopN) cannotImprove(c models.Chunk, backward bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.topN.full() {
		return false
	}
	w := s.topN.h.es[0].TS
	if backward {
		return c.MaxTS < w // equal timestamps may still win the tie-break
	}
	return c.MinTS > w
}

// --- planning ---

const inChunk = 500

// resolveStreams returns the labels of every stream satisfying the selector.
// Positive matchers narrow candidates through the indexed stream_labels table;
// all matchers (including negative ones) are then re-checked against the labels.
func (s *Service) resolveStreams(ctx context.Context, q *Query, pipe *Pipeline) (map[uint64]map[string]string, error) {
	var cand map[uint64]struct{}
	intersect := func(ids []uint64) {
		next := make(map[uint64]struct{}, len(ids))
		for _, id := range ids {
			if cand == nil {
				next[id] = struct{}{}
			} else if _, ok := cand[id]; ok {
				next[id] = struct{}{}
			}
		}
		cand = next
	}
	for _, m := range pipe.Matchers {
		var ids []uint64
		switch {
		case m.Op == OpEq && m.Value != "":
			err := s.DB.WithContext(ctx).Model(&models.StreamLabel{}).
				Where("key = ? AND value = ?", m.Name, m.Value).Pluck("stream_id", &ids).Error
			if err != nil {
				return nil, err
			}
		case m.Op == OpRe && !m.re.MatchString(""):
			var vals []string
			if err := s.DB.WithContext(ctx).Model(&models.StreamLabel{}).Where("key = ?", m.Name).
				Distinct().Pluck("value", &vals).Error; err != nil {
				return nil, err
			}
			var hit []string
			for _, v := range vals {
				if m.re.MatchString(v) {
					hit = append(hit, v)
				}
			}
			for i := 0; i < len(hit); i += inChunk {
				var part []uint64
				err := s.DB.WithContext(ctx).Model(&models.StreamLabel{}).
					Where("key = ? AND value IN ?", m.Name, hit[i:min(i+inChunk, len(hit))]).Pluck("stream_id", &part).Error
				if err != nil {
					return nil, err
				}
				ids = append(ids, part...)
			}
		default:
			continue
		}
		intersect(ids)
		if len(cand) == 0 {
			return nil, nil
		}
	}
	out := make(map[uint64]map[string]string, len(cand))
	list := make([]uint64, 0, len(cand))
	for id := range cand {
		list = append(list, id)
	}
	for i := 0; i < len(list); i += inChunk {
		var rows []models.Stream
		err := s.DB.WithContext(ctx).Select("id", "labels").Where("id IN ?", list[i:min(i+inChunk, len(list))]).Find(&rows).Error
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			labels, err := decodeLabels(r.Labels)
			if err != nil {
				return nil, err
			}
			if pipe.MatchLabels(labels) {
				out[r.ID] = labels
			}
		}
	}
	return out, nil
}

// listChunks returns chunks of the given streams overlapping [start, end).
func (s *Service) listChunks(ctx context.Context, ids []uint64, req Request) ([]models.Chunk, error) {
	var all []models.Chunk
	for i := 0; i < len(ids); i += inChunk {
		var part []models.Chunk
		err := s.DB.WithContext(ctx).
			Where("stream_id IN ? AND max_ts >= ? AND min_ts < ?", ids[i:min(i+inChunk, len(ids))], req.Start, req.End).
			Find(&part).Error
		if err != nil {
			return nil, err
		}
		all = append(all, part...)
	}
	return all, nil
}

// FormatStreamID renders a stream id for JSON (uint64 exceeds JS number precision).
func FormatStreamID(id uint64) string { return strconv.FormatUint(id, 10) }

package query

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cragone/Super-Argus/internal/chunk"
	"github.com/cragone/Super-Argus/internal/db"
	"github.com/cragone/Super-Argus/internal/index"
	"github.com/cragone/Super-Argus/internal/models"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

var base = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

func at(sec int) int64 { return base.Add(time.Duration(sec) * time.Second).UnixNano() }

type fakeHeads struct {
	mu    sync.Mutex
	heads []chunk.HeadSnapshot
}

func (f *fakeHeads) SnapshotHeads(streams map[uint64]struct{}, from, to int64) []chunk.HeadSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []chunk.HeadSnapshot
	for _, h := range f.heads {
		if _, ok := streams[h.StreamID]; !ok {
			continue
		}
		var es []chunk.Entry
		for _, e := range h.Entries {
			if e.TS >= from && e.TS < to {
				es = append(es, e)
			}
		}
		if len(es) > 0 {
			out = append(out, chunk.HeadSnapshot{StreamID: h.StreamID, ChunkID: h.ChunkID, Entries: es})
		}
	}
	return out
}

type loaded struct {
	service, env string
	e            chunk.Entry
}

type fixture struct {
	t     *testing.T
	g     *gorm.DB
	ix    *index.Index
	store *chunk.LocalStore
	svc   *Service
	heads *fakeHeads
	ids   map[string]uint64 // "svc/env" -> stream id
	n     int
	all   []loaded          // every line written by load(), for brute-force comparisons
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	g, err := db.Open("sqlite:" + filepath.Join(dir, "t.db"))
	require.NoError(t, err)
	sqlDB, _ := g.DB()
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.Migrate(g))
	f := &fixture{t: t, g: g, ix: index.New(g), store: chunk.NewLocalStore(filepath.Join(dir, "chunks")),
		heads: &fakeHeads{}, ids: map[string]uint64{}}
	f.svc = NewService(g, f.store, f.heads, DefaultLimits())
	return f
}

func (f *fixture) stream(service, env string) uint64 {
	key := service + "/" + env
	if id, ok := f.ids[key]; ok {
		return id
	}
	id, err := f.ix.Resolve("k", map[string]string{"service": service, "env": env}, 0)
	require.NoError(f.t, err)
	f.ids[key] = id
	return id
}

// addChunk writes entries as one chunk file and indexes it; returns the chunk id.
func (f *fixture) addChunk(stream uint64, es []chunk.Entry) string {
	f.t.Helper()
	data, meta, err := chunk.Encode(stream, es)
	require.NoError(f.t, err)
	f.n++
	id := fmt.Sprintf("chunk-%03d", f.n)
	key := chunk.Key(meta.MinTS, stream, id)
	require.NoError(f.t, f.store.Put(key, data))
	require.NoError(f.t, f.ix.AddChunk(&models.Chunk{ID: id, StreamID: stream, Path: key, MinTS: meta.MinTS, MaxTS: meta.MaxTS,
		LineCount: meta.Lines, BytesCompressed: int64(meta.BytesCompressed), BytesRaw: int64(meta.BytesRaw)}))
	return id
}

// load builds a deterministic dataset: 3 services x 2 envs, 60 lines each, one per second,
// split over two chunks (t 0..29 and 30..59).
func (f *fixture) load() {
	levels := []string{"info", "info", "warn", "error"}
	for _, svc := range []string{"api", "web", "worker"} {
		for _, env := range []string{"prod", "dev"} {
			id := f.stream(svc, env)
			for half := 0; half < 2; half++ {
				var es []chunk.Entry
				for i := half * 30; i < half*30+30; i++ {
					lvl := levels[i%4]
					var line string
					switch svc {
					case "api":
						line = fmt.Sprintf(`{"level":%q,"status":%d,"route":"/v1/item/%d","msg":"handled request %d"}`, lvl, 200+100*(i%6), i%5, i)
					case "web":
						line = fmt.Sprintf(`level=%s status=%d msg="page %d rendered" path=/p/%d`, lvl, 200+100*(i%6), i, i%3)
					default:
						line = fmt.Sprintf("job %d finished level=%s", i, lvl)
					}
					if i%10 == 7 {
						line += " timeout"
					}
					es = append(es, chunk.Entry{TS: at(i), Line: line})
					f.all = append(f.all, loaded{svc, env, chunk.Entry{TS: at(i), Line: line}})
				}
				f.addChunk(id, es)
			}
		}
	}
}

func (f *fixture) run(q string, start, end int, limit int, backward bool) *Result {
	f.t.Helper()
	res, err := f.svc.Range(context.Background(), "u1", Request{Query: q, Start: at(start), End: at(end), Limit: limit, Backward: backward})
	require.NoError(f.t, err, q)
	return res
}

func render(res *Result) string {
	var b strings.Builder
	for _, e := range res.Entries {
		l := res.Streams[e.Stream]
		fmt.Fprintf(&b, "%s %s/%s %s", time.Unix(0, e.TS).UTC().Format("15:04:05"), l["service"], l["env"], e.Line)
		if len(e.Fields) > 0 {
			keys := make([]string, 0, len(e.Fields))
			for k := range e.Fields {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			b.WriteString("  {")
			for i, k := range keys {
				if i > 0 {
					b.WriteString(", ")
				}
				fmt.Fprintf(&b, "%s=%s", k, e.Fields[k])
			}
			b.WriteString("}")
		}
		b.WriteString("\n")
	}
	return b.String()
}

var execCases = []struct {
	q          string
	start, end int
	limit      int
	backward   bool
}{
	{`{service="api", env="prod"}`, 0, 60, 5, true},
	{`{service="api", env="prod"}`, 0, 60, 5, false},
	{`{service="api", env="prod"}`, 25, 35, 100, false}, // spans the chunk boundary
	{`{service="api", env="prod"} |= "timeout"`, 0, 60, 100, false},
	{`{service="api", env="prod"} |= "timeout" != "request 7"`, 0, 60, 100, false},
	{`{service=~"api|web", env="dev"} |~ "(request|page) (1|2)\\d\\b"`, 0, 60, 100, false},
	{`{service="api"} | json | level="error" | status>=500`, 0, 24, 100, false},
	{`{service="api", env="prod"} | json | route="/v1/item/3" | status!=200`, 0, 20, 100, false},
	{`{service="web", env="prod"} | logfmt | level="warn" | path=~"/p/[01]"`, 0, 30, 100, false},
	{`{service="web", env="prod"} | logfmt | status>=500 | status<600`, 0, 12, 100, false},
	{`{service="worker", env="dev"} |= "level=error"`, 0, 20, 100, true},
	{`{env!="dev", service="worker"} |= "timeout"`, 0, 60, 100, false},
	{`{service=~"web|worker", env!~"d.*"} | logfmt | level="error"`, 0, 12, 100, false},
	{`{service="nope"}`, 0, 60, 100, false},
	{`{service="api", env="prod"} | json | level="fatal"`, 0, 60, 100, false},
}

func TestExecutorGolden(t *testing.T) {
	f := newFixture(t)
	f.load()
	var b strings.Builder
	for _, c := range execCases {
		dir := "forward"
		if c.backward {
			dir = "backward"
		}
		res := f.run(c.q, c.start, c.end, c.limit, c.backward)
		fmt.Fprintf(&b, "query: %s  [%d,%d) limit=%d %s\n%s\n", c.q, c.start, c.end, c.limit, dir, render(res))
	}
	golden(t, "exec.golden", b.String())
}

func TestStatsAndChunkPruning(t *testing.T) {
	f := newFixture(t)
	f.load()

	// 6 streams x 2 chunks, but a selector hits one stream and a range covers one chunk
	res := f.run(`{service="api", env="prod"}`, 0, 10, 100, false)
	require.Len(t, res.Entries, 10)
	require.Equal(t, 1, res.Stats.Streams)
	require.Equal(t, 1, res.Stats.ChunksScanned)
	require.EqualValues(t, 10, res.Stats.LinesScanned, "block/range pruning: only in-range lines are read")

	res = f.run(`{service=~".+"}`, 0, 60, 100, true)
	require.Len(t, res.Entries, 100)
	require.Equal(t, 6, res.Stats.Streams)
}

func TestBackwardEarlyStopSkipsOldChunks(t *testing.T) {
	f := newFixture(t)
	id := f.stream("api", "prod")
	for c := 0; c < 20; c++ {
		var es []chunk.Entry
		for i := 0; i < 50; i++ {
			es = append(es, chunk.Entry{TS: at(c*100 + i), Line: fmt.Sprintf("c%d-%d", c, i)})
		}
		f.addChunk(id, es)
	}
	f.svc.Limits.Workers = 1 // deterministic ordering for the skip count
	res := f.run(`{service="api"}`, 0, 5000, 10, true)
	require.Len(t, res.Entries, 10)
	require.Equal(t, "c19-49", res.Entries[0].Line)
	require.Equal(t, 1, res.Stats.ChunksScanned)
	require.Equal(t, 19, res.Stats.ChunksSkipped)

	res = f.run(`{service="api"}`, 0, 5000, 10, false)
	require.Equal(t, "c0-0", res.Entries[0].Line)
	require.Equal(t, 1, res.Stats.ChunksScanned)
	require.Equal(t, 19, res.Stats.ChunksSkipped)
}

func TestHeadsAreMerged(t *testing.T) {
	f := newFixture(t)
	id := f.stream("api", "prod")
	f.addChunk(id, []chunk.Entry{{TS: at(1), Line: "flushed 1"}, {TS: at(2), Line: "flushed 2"}})
	f.heads.heads = []chunk.HeadSnapshot{{StreamID: id, ChunkID: "unflushed", Entries: []chunk.Entry{{TS: at(3), Line: "live 3"}, {TS: at(4), Line: "live 4"}}}}

	res := f.run(`{service="api"}`, 0, 10, 100, false)
	var lines []string
	for _, e := range res.Entries {
		lines = append(lines, e.Line)
	}
	require.Equal(t, []string{"flushed 1", "flushed 2", "live 3", "live 4"}, lines)
	require.Equal(t, 2, res.Stats.HeadLines)

	// pipeline filters apply to head lines too
	res = f.run(`{service="api"} |= "live"`, 0, 10, 100, true)
	require.Len(t, res.Entries, 2)
	require.Equal(t, "live 4", res.Entries[0].Line)
}

func TestHeadAlreadyFlushedIsNotDuplicated(t *testing.T) {
	f := newFixture(t)
	id := f.stream("api", "prod")
	cid := f.addChunk(id, []chunk.Entry{{TS: at(1), Line: "a"}, {TS: at(2), Line: "b"}})
	// the snapshot was taken just before the flush finished: same lines, tagged with the chunk's id
	f.heads.heads = []chunk.HeadSnapshot{{StreamID: id, ChunkID: cid, Entries: []chunk.Entry{{TS: at(1), Line: "a"}, {TS: at(2), Line: "b"}}}}
	res := f.run(`{service="api"}`, 0, 10, 100, false)
	require.Len(t, res.Entries, 2)
}

func TestLimitsAndValidation(t *testing.T) {
	f := newFixture(t)
	f.load()
	ctx := context.Background()
	call := func(r Request) error { _, err := f.svc.Range(ctx, "u1", r); return err }

	require.ErrorIs(t, call(Request{Query: `{service="api"}`}), ErrBadRequest, "time range is required")
	require.ErrorIs(t, call(Request{Query: `{service="api"}`, Start: at(10), End: at(5)}), ErrBadRequest)
	require.ErrorIs(t, call(Request{Query: `{service=~".*"}`, Start: at(0), End: at(5)}), ErrBadRequest, "empty-matching selector")
	require.ErrorIs(t, call(Request{Query: `nonsense`, Start: at(0), End: at(5)}), ErrBadRequest)

	f.svc.Limits.MaxRange = time.Hour
	err := call(Request{Query: `{service="api"}`, Start: at(0), End: at(2 * 3600)})
	require.ErrorIs(t, err, ErrLimit)
	require.Contains(t, err.Error(), "1h0m0s")
	f.svc.Limits.MaxRange = 0

	// limit defaults and caps
	f.svc.Limits.DefaultLimit, f.svc.Limits.MaxLimit = 7, 9
	res := f.run(`{service=~".+"}`, 0, 60, 0, false)
	require.Len(t, res.Entries, 7)
	res = f.run(`{service=~".+"}`, 0, 60, 1000, false)
	require.Len(t, res.Entries, 9)

	// scan budget
	f.svc.Limits.MaxBytesScanned = 200
	err = call(Request{Query: `{service=~".+"}`, Start: at(0), End: at(60)})
	require.ErrorIs(t, err, ErrLimit)
	require.Contains(t, err.Error(), "scanned more than")
}

func TestTimeout(t *testing.T) {
	f := newFixture(t)
	f.load()
	f.svc.Limits.Timeout = time.Nanosecond
	_, err := f.svc.Range(context.Background(), "u1", Request{Query: `{service=~".+"}`, Start: at(0), End: at(60)})
	require.ErrorIs(t, err, ErrTimeout)
}

func TestPerUserConcurrencyCap(t *testing.T) {
	f := newFixture(t)
	f.svc.Limits.PerUserConcurrency = 2
	r1, err := f.svc.acquire("u1")
	require.NoError(t, err)
	r2, err := f.svc.acquire("u1")
	require.NoError(t, err)
	_, err = f.svc.acquire("u1")
	require.ErrorIs(t, err, ErrBusy)
	r3, err := f.svc.acquire("u2")
	require.NoError(t, err, "other users are unaffected")
	r1()
	r4, err := f.svc.acquire("u1")
	require.NoError(t, err, "slot freed")
	for _, r := range []func(){r2, r3, r4} {
		r()
	}
	require.Empty(t, f.svc.active)
}

func TestMissingChunkFileIsSkipped(t *testing.T) {
	f := newFixture(t)
	id := f.stream("api", "prod")
	f.addChunk(id, []chunk.Entry{{TS: at(1), Line: "keep"}})
	var c models.Chunk
	require.NoError(t, f.g.First(&c).Error)
	require.NoError(t, f.store.Delete(c.Path)) // retention raced the query
	res := f.run(`{service="api"}`, 0, 10, 10, false)
	require.Empty(t, res.Entries)
}

func TestLabels(t *testing.T) {
	f := newFixture(t)
	f.load()
	require.NoError(t, f.ix.FlushSeen())
	ctx := context.Background()

	names, err := f.svc.Labels(ctx, 0, 0)
	require.NoError(t, err)
	require.Equal(t, []string{"env", "service"}, names)

	vals, err := f.svc.LabelValues(ctx, "service", 0, 0)
	require.NoError(t, err)
	require.Equal(t, []string{"api", "web", "worker"}, vals)

	// streams last seen before `start` are excluded
	vals, err = f.svc.LabelValues(ctx, "env", time.Now().Add(time.Hour).UnixNano(), 0)
	require.NoError(t, err)
	require.Empty(t, vals)

	_, err = f.svc.LabelValues(ctx, "", 0, 0)
	require.ErrorIs(t, err, ErrBadRequest)
}

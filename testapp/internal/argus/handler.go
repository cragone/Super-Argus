package argus

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
)

// Handler returns a slog.Handler that writes each record as one JSON line
// ({"time","level","msg",...attrs}, with a lowercase level) to the shipper, so
// Argus queries can use `| json | level="error"`. A nil Shipper yields a handler that drops everything.
func (s *Shipper) Handler(level slog.Leveler) slog.Handler {
	return &jsonHandler{s: s, level: level}
}

type jsonHandler struct {
	s     *Shipper
	level slog.Leveler
	ops   []func(slog.Handler) slog.Handler // WithAttrs / WithGroup, replayed per record
}

func (h *jsonHandler) Enabled(_ context.Context, l slog.Level) bool {
	return h.s != nil && l >= h.level.Level()
}

func (h *jsonHandler) Handle(ctx context.Context, r slog.Record) error {
	var buf bytes.Buffer
	var inner slog.Handler = slog.NewJSONHandler(&buf, &slog.HandlerOptions{
		Level: slog.Level(-100),
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) == 0 && a.Key == slog.LevelKey {
				a.Value = slog.StringValue(strings.ToLower(a.Value.String()))
			}
			return a
		},
	})
	for _, op := range h.ops {
		inner = op(inner)
	}
	if err := inner.Handle(ctx, r); err != nil {
		return err
	}
	h.s.Send(r.Time, strings.TrimRight(buf.String(), "\n"))
	return nil
}

func (h *jsonHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	c := *h
	c.ops = append(append([]func(slog.Handler) slog.Handler(nil), h.ops...), func(x slog.Handler) slog.Handler { return x.WithAttrs(attrs) })
	return &c
}

func (h *jsonHandler) WithGroup(name string) slog.Handler {
	c := *h
	c.ops = append(append([]func(slog.Handler) slog.Handler(nil), h.ops...), func(x slog.Handler) slog.Handler { return x.WithGroup(name) })
	return &c
}

// Tee fans a record out to several handlers (for example the console and Argus).
func Tee(handlers ...slog.Handler) slog.Handler { return tee(handlers) }

type tee []slog.Handler

func (t tee) Enabled(ctx context.Context, l slog.Level) bool {
	for _, h := range t {
		if h.Enabled(ctx, l) {
			return true
		}
	}
	return false
}

func (t tee) Handle(ctx context.Context, r slog.Record) error {
	for _, h := range t {
		if h.Enabled(ctx, r.Level) {
			_ = h.Handle(ctx, r.Clone())
		}
	}
	return nil
}

func (t tee) WithAttrs(a []slog.Attr) slog.Handler {
	out := make(tee, len(t))
	for i, h := range t {
		out[i] = h.WithAttrs(a)
	}
	return out
}

func (t tee) WithGroup(n string) slog.Handler {
	out := make(tee, len(t))
	for i, h := range t {
		out[i] = h.WithGroup(n)
	}
	return out
}

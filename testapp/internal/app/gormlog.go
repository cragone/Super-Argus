package app

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// SlowQuery is the duration above which a query is logged as a warning.
const SlowQuery = 200 * time.Millisecond

// gormLogger sends GORM's SQL trace through slog, so every query shows up in Argus
// with its duration and row count.
type gormLogger struct {
	log   *slog.Logger
	level gormlogger.LogLevel
}

// NewGormLogger adapts a slog.Logger to GORM's logger interface.
func NewGormLogger(l *slog.Logger) gormlogger.Interface {
	return &gormLogger{log: l.With("component", "gorm"), level: gormlogger.Info}
}

func (g *gormLogger) LogMode(level gormlogger.LogLevel) gormlogger.Interface {
	c := *g
	c.level = level
	return &c
}

func (g *gormLogger) Info(ctx context.Context, msg string, args ...any) {
	if g.level >= gormlogger.Info {
		g.log.InfoContext(ctx, msg, "args", args)
	}
}

func (g *gormLogger) Warn(ctx context.Context, msg string, args ...any) {
	if g.level >= gormlogger.Warn {
		g.log.WarnContext(ctx, msg, "args", args)
	}
}

func (g *gormLogger) Error(ctx context.Context, msg string, args ...any) {
	if g.level >= gormlogger.Error {
		g.log.ErrorContext(ctx, msg, "args", args)
	}
}

func (g *gormLogger) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	if g.level <= gormlogger.Silent {
		return
	}
	elapsed := time.Since(begin)
	sql, rows := fc()
	attrs := []any{"sql", sql, "rows", rows, "duration_ms", elapsed.Milliseconds()}
	switch {
	case err != nil && !errors.Is(err, gorm.ErrRecordNotFound):
		g.log.ErrorContext(ctx, "query failed", append(attrs, "error", err.Error())...)
	case elapsed > SlowQuery:
		g.log.WarnContext(ctx, "slow query", attrs...)
	case g.level >= gormlogger.Info:
		g.log.InfoContext(ctx, "query", attrs...)
	}
}

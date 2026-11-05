package app

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/gin-gonic/gin"
)

const requestIDHeader = "X-Request-Id"

// RequestID uses the caller's X-Request-Id or makes one, and echoes it back.
// The id goes in the log line, never in a label (it is unbounded).
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader(requestIDHeader)
		if id == "" || len(id) > 64 {
			b := make([]byte, 8)
			_, _ = rand.Read(b)
			id = hex.EncodeToString(b)
		}
		c.Set("request_id", id)
		c.Header(requestIDHeader, id)
		c.Next()
	}
}

func requestID(c *gin.Context) string { return c.GetString("request_id") }

// RequestLogger writes one structured line per request: msg "request" with
// method, route template, status and latency. 5xx are errors, 4xx warnings.
func RequestLogger(log *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		route := c.FullPath()
		if route == "" {
			route = "unmatched" // keep the value bounded: never log raw paths for 404s as a grouping key
		}
		status := c.Writer.Status()
		level := slog.LevelInfo
		switch {
		case status >= 500:
			level = slog.LevelError
		case status >= 400:
			level = slog.LevelWarn
		}
		log.LogAttrs(c.Request.Context(), level, "request",
			slog.String("request_id", requestID(c)),
			slog.String("method", c.Request.Method),
			slog.String("route", route),
			slog.String("path", c.Request.URL.Path),
			slog.Int("status", status),
			slog.Int64("latency_ms", time.Since(start).Milliseconds()),
			slog.Int("bytes", c.Writer.Size()),
			slog.String("client_ip", c.ClientIP()),
		)
	}
}

// Recovery turns a panic into a logged error with a stack trace and a 500.
func Recovery(log *slog.Logger) gin.HandlerFunc {
	return gin.CustomRecovery(func(c *gin.Context, err any) {
		stack := string(debug.Stack())
		if len(stack) > 4000 {
			stack = stack[:4000]
		}
		log.ErrorContext(c.Request.Context(), "panic recovered",
			slog.String("request_id", requestID(c)),
			slog.String("route", c.FullPath()),
			slog.Any("panic", err),
			slog.String("stack", stack),
		)
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "internal error", "request_id": requestID(c)})
	})
}

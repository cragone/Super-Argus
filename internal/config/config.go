// Package config loads Argus configuration from environment variables.
package config

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// Config holds runtime settings.
type Config struct {
	DataDir       string
	HTTPAddr      string
	DBDSN         string
	RetentionDays int
	JWTSecret     string
	AdminEmail    string
	AdminPassword string
	WALSyncMS     int
	QueryTimeout  time.Duration
	MaxStreams    int
	RateLines     int // default per-key lines/sec
	RateBytes     int // per-key bytes/sec
	QueryMaxRange time.Duration
	QueryMaxBytes int64
	QueryConc     int // concurrent queries per user
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// Load reads configuration from the environment.
func Load() (*Config, error) {
	c := &Config{
		DataDir:       env("ARGUS_DATA_DIR", "./data"),
		HTTPAddr:      env("ARGUS_HTTP_ADDR", ":8080"),
		RetentionDays: envInt("ARGUS_RETENTION_DAYS", 14),
		JWTSecret:     os.Getenv("ARGUS_JWT_SECRET"),
		AdminEmail:    os.Getenv("ARGUS_ADMIN_EMAIL"),
		AdminPassword: os.Getenv("ARGUS_ADMIN_PASSWORD"),
		WALSyncMS:     envInt("ARGUS_WAL_SYNC_MS", 200),
		MaxStreams:    envInt("ARGUS_MAX_STREAMS_PER_KEY", 10000),
		RateLines:     envInt("ARGUS_RATE_LINES_PER_SEC", 10000),
		RateBytes:     envInt("ARGUS_RATE_BYTES_PER_SEC", 10<<20),
	}
	d, err := time.ParseDuration(env("ARGUS_QUERY_TIMEOUT", "30s"))
	if err != nil {
		return nil, err
	}
	c.QueryTimeout = d
	if c.QueryMaxRange, err = time.ParseDuration(env("ARGUS_QUERY_MAX_RANGE", "168h")); err != nil {
		return nil, err
	}
	c.QueryMaxBytes = int64(envInt("ARGUS_QUERY_MAX_BYTES_MB", 4096)) << 20
	c.QueryConc = envInt("ARGUS_QUERY_CONCURRENCY", 4)
	c.DBDSN = env("ARGUS_DB_DSN", "sqlite:"+filepath.Join(c.DataDir, "argus.db"))
	if c.JWTSecret == "" {
		return nil, errors.New("ARGUS_JWT_SECRET is required")
	}
	return c, nil
}

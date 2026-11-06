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
	DataDir          string
	HTTPAddr         string
	DBDSN            string
	RetentionDays    int
	JWTSecret        string
	AdminEmail       string
	AdminPassword    string
	WALSyncMS        int
	QueryTimeout     time.Duration
	MaxStreams       int
	RateLines        int // default per-key lines/sec
	RateBytes        int // per-key bytes/sec
	QueryMaxRange    time.Duration
	QueryMaxBytes    int64
	QueryConc        int // concurrent queries per user
	QueryMetricRange time.Duration
	CORSOrigins      string
	TailPerUser      int
	TailMax          int
	TLSCert          string // ARGUS_TLS_CERT / ARGUS_TLS_KEY: serve HTTPS when both are set
	TLSKey           string
	MetricsToken     string // ARGUS_METRICS_TOKEN: bearer token for /metrics scrapers
	Redact           string // ARGUS_REDACT: email,bearer,card,ip or all
	Sample           string // ARGUS_SAMPLE: service=percent,...
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

// Load reads configuration from the environment for the server.
func Load() (*Config, error) { return load(true) }

// LoadTool reads configuration for command-line tools such as backup, which
// do not need the JWT secret.
func LoadTool() (*Config, error) { return load(false) }

func load(needSecret bool) (*Config, error) {
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
	if c.QueryMetricRange, err = time.ParseDuration(env("ARGUS_QUERY_METRIC_MAX_RANGE", "744h")); err != nil {
		return nil, err
	}
	c.QueryMaxBytes = int64(envInt("ARGUS_QUERY_MAX_BYTES_MB", 4096)) << 20
	c.QueryConc = envInt("ARGUS_QUERY_CONCURRENCY", 4)
	c.CORSOrigins = os.Getenv("ARGUS_CORS_ORIGINS")
	c.TailPerUser = envInt("ARGUS_TAIL_PER_USER", 5)
	c.TailMax = envInt("ARGUS_TAIL_MAX", 500)
	c.TLSCert, c.TLSKey = os.Getenv("ARGUS_TLS_CERT"), os.Getenv("ARGUS_TLS_KEY")
	if (c.TLSCert == "") != (c.TLSKey == "") {
		return nil, errors.New("ARGUS_TLS_CERT and ARGUS_TLS_KEY must be set together")
	}
	c.MetricsToken = os.Getenv("ARGUS_METRICS_TOKEN")
	c.Redact = os.Getenv("ARGUS_REDACT")
	c.Sample = os.Getenv("ARGUS_SAMPLE")
	c.DBDSN = env("ARGUS_DB_DSN", "sqlite:"+filepath.Join(c.DataDir, "argus.db"))
	if needSecret && c.JWTSecret == "" {
		return nil, errors.New("ARGUS_JWT_SECRET is required")
	}
	return c, nil
}

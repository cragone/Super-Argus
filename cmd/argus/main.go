// Command argus runs the Argus log server.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/joho/godotenv"

	"github.com/cragone/Super-Argus/internal/api"
	"github.com/cragone/Super-Argus/internal/auth"
	"github.com/cragone/Super-Argus/internal/config"
	"github.com/cragone/Super-Argus/internal/db"
)

// loadDotEnv loads the nearest .env found in the working directory or any
// parent. Variables already set in the environment are not overridden.
func loadDotEnv() {
	dir, err := os.Getwd()
	if err != nil {
		return
	}
	for {
		p := filepath.Join(dir, ".env")
		if _, err := os.Stat(p); err == nil {
			_ = godotenv.Load(p)
			return
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return
		}
		dir = parent
	}
}

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	loadDotEnv()
	cfg, err := config.Load()
	if err != nil {
		log.Error("config", "err", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		log.Error("data dir", "err", err)
		os.Exit(1)
	}
	g, err := db.Open(cfg.DBDSN)
	if err != nil {
		log.Error("db open", "err", err)
		os.Exit(1)
	}
	if err := db.Migrate(g); err != nil {
		log.Error("migrate", "err", err)
		os.Exit(1)
	}
	if err := auth.SeedAdmin(g, cfg.AdminEmail, cfg.AdminPassword, log); err != nil {
		log.Error("seed admin", "err", err)
		os.Exit(1)
	}
	handler := api.NewRouter(&api.Server{DB: g, Tokens: auth.NewTokenManager(cfg.JWTSecret)})
	srv := &http.Server{Addr: cfg.HTTPAddr, Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		log.Info("listening", "addr", cfg.HTTPAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server", "err", err)
			os.Exit(1)
		}
	}()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	sctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = srv.Shutdown(sctx)
}

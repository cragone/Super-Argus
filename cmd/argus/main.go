// Command argus runs the Argus log server.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/cragone/Super-Argus/internal/api"
	"github.com/cragone/Super-Argus/internal/config"
	"github.com/cragone/Super-Argus/internal/db"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
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
	srv := &http.Server{Addr: cfg.HTTPAddr, Handler: api.NewRouter(), ReadHeaderTimeout: 10 * time.Second}
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

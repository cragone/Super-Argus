// Command testapp is a local gin + GORM service that ships its logs to Argus,
// so you can watch a real application's traffic in the Argus UI.
//
//	ARGUS_URL      Argus base URL            (default http://localhost:8080)
//	ARGUS_API_KEY  ingest key                (unset: logs go to the console only)
//	TESTAPP_ADDR   listen address            (default :9090)
//	TESTAPP_DB     SQLite file               (default ./testapp.db)
//	TESTAPP_ENV    value of the env label    (default local)
package main

import (
	"context"
	"errors"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/cragone/Super-Argus/testapp/internal/app"
	"github.com/cragone/Super-Argus/testapp/internal/argus"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	gin.SetMode(gin.ReleaseMode)

	ship := argus.New(argus.Config{
		URL:    env("ARGUS_URL", "http://localhost:8080"),
		APIKey: os.Getenv("ARGUS_API_KEY"),
		// Low-cardinality labels only; request ids and item ids stay in the line.
		Labels: map[string]string{"service": "testapp", "env": env("TESTAPP_ENV", "local")},
	})
	console := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})
	logger := slog.New(argus.Tee(console, ship.Handler(slog.LevelDebug)))
	logger.Info("starting", "shipping", ship.String())
	if !ship.Enabled() {
		log.Println("ARGUS_API_KEY is not set: logs are printed here but not sent to Argus")
	}

	db, err := gorm.Open(sqlite.Open(env("TESTAPP_DB", "testapp.db")), &gorm.Config{Logger: app.NewGormLogger(logger)})
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	if err := app.Migrate(db); err != nil {
		log.Fatalf("migrate: %v", err)
	}

	a := &app.App{DB: db, Log: logger}
	srv := &http.Server{Addr: env("TESTAPP_ADDR", ":9090"), Handler: a.Router(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		logger.Info("listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server: %v", err)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()

	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(sctx)
	logger.Info("shutting down", "sent", ship.Stats().Sent, "dropped", ship.Stats().Dropped)
	ship.Close(sctx) // flush queued lines before exiting
}

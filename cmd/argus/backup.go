package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"

	"github.com/cragone/Super-Argus/internal/backup"
	"github.com/cragone/Super-Argus/internal/config"
	"github.com/cragone/Super-Argus/internal/db"
)

const usage = `usage: argus [command]

  (no command)       run the server
  backup <dir>       copy the data directory to <dir> while the server runs
                     (incremental: run it again to the same <dir> to update it)
`

// runCommand runs a subcommand and returns the process exit code.
func runCommand(name string, args []string, log *slog.Logger) int {
	switch name {
	case "backup":
		return runBackup(args, log)
	case "help", "-h", "--help":
		fmt.Print(usage)
		return 0
	}
	fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", name, usage)
	return 2
}

func runBackup(args []string, log *slog.Logger) int {
	if len(args) != 1 {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}
	cfg, err := config.LoadTool()
	if err != nil {
		log.Error("config", "err", err)
		return 1
	}
	g, err := db.Open(cfg.DBDSN)
	if err != nil {
		log.Error("db open", "err", err)
		return 1
	}
	if sqlDB, err := g.DB(); err == nil {
		defer sqlDB.Close()
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	res, err := backup.Run(ctx, g, cfg.DataDir, args[0])
	if err != nil {
		log.Error("backup failed", "err", err)
		return 1
	}
	log.Info("backup complete", "dest", args[0], "db_bytes", res.DBBytes, "wal_files", res.WALFiles,
		"chunks_copied", res.ChunksCopied, "chunks_unchanged", res.ChunksSkipped, "chunks_missing", res.MissingChunks)
	return 0
}

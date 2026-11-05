// Package db sets up the GORM connection and runs embedded goose migrations.
package db

import (
	"database/sql"
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/glebarez/sqlite"
	"github.com/pressly/goose/v3"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Open connects to the database described by dsn ("sqlite:<path>" or "postgres://...").
func Open(dsn string) (*gorm.DB, error) {
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		return gorm.Open(postgres.Open(dsn), &gorm.Config{})
	}
	path := strings.TrimPrefix(dsn, "sqlite:")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	d := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)", filepath.ToSlash(path))
	return gorm.Open(sqlite.Open(d), &gorm.Config{})
}

// Migrate applies all embedded goose migrations.
func Migrate(g *gorm.DB) error {
	sqlDB, err := g.DB()
	if err != nil {
		return err
	}
	return migrateSQL(sqlDB, g.Dialector.Name())
}

func migrateSQL(sqlDB *sql.DB, dialect string) error {
	goose.SetBaseFS(migrationsFS)
	if dialect == "postgres" {
		dialect = "postgres"
	} else {
		dialect = "sqlite3"
	}
	if err := goose.SetDialect(dialect); err != nil {
		return err
	}
	return goose.Up(sqlDB, "migrations")
}

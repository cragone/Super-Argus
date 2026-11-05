package db

import (
	"path/filepath"
	"testing"
)

func TestOpenAndMigrate(t *testing.T) {
	g, err := Open("sqlite:" + filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := g.DB()
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := Migrate(g); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := g.Raw("SELECT count(*) FROM settings").Scan(&n).Error; err != nil {
		t.Fatal(err)
	}
}

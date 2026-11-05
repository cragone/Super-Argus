package config

import "testing"

func TestLoadRequiresSecret(t *testing.T) {
	t.Setenv("ARGUS_JWT_SECRET", "")
	if _, err := Load(); err == nil {
		t.Fatal("expected error without JWT secret")
	}
}

func TestLoadDefaults(t *testing.T) {
	t.Setenv("ARGUS_JWT_SECRET", "s")
	t.Setenv("ARGUS_DATA_DIR", "")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.HTTPAddr != ":8080" || c.RetentionDays != 14 || c.WALSyncMS != 200 {
		t.Fatalf("bad defaults: %+v", c)
	}
}

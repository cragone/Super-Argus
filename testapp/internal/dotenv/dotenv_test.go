package dotenv

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParse(t *testing.T) {
	cases := []struct {
		in, k, v string
		ok       bool
	}{
		{"A=1", "A", "1", true},
		{"  A = 1  ", "A", "1", true},
		{"export A=1", "A", "1", true},
		{`A="x y"`, "A", "x y", true},
		{`A='x # y'`, "A", "x # y", true},
		{"A=1 # note", "A", "1", true},
		{"A=", "A", "", true},
		{"A=b=c", "A", "b=c", true},
		{"# comment", "", "", false},
		{"", "", "", false},
		{"novalue", "", "", false},
		{"=x", "", "", false},
	}
	for _, c := range cases {
		k, v, ok := parse(c.in)
		require.Equal(t, c.ok, ok, c.in)
		if ok {
			require.Equal(t, c.k, k, c.in)
			require.Equal(t, c.v, v, c.in)
		}
	}
}

func TestLoadDoesNotOverrideEnvironment(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".env")
	require.NoError(t, os.WriteFile(p, []byte("DOTENV_T_NEW=from-file\nDOTENV_T_SET=from-file\n"), 0o600))
	t.Setenv("DOTENV_T_SET", "from-env")
	os.Unsetenv("DOTENV_T_NEW")
	t.Cleanup(func() { os.Unsetenv("DOTENV_T_NEW") })

	require.NoError(t, Load(p))
	require.Equal(t, "from-file", os.Getenv("DOTENV_T_NEW"))
	require.Equal(t, "from-env", os.Getenv("DOTENV_T_SET"))
	require.NoError(t, Load(filepath.Join(t.TempDir(), "missing.env")))
}

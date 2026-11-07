// Package dotenv loads KEY=VALUE pairs from a .env file into the process
// environment, so the test app and traffic generator run with a bare `go run`.
package dotenv

import (
	"bufio"
	"os"
	"strings"
)

// Load reads path and sets each variable that is not already set, so real
// environment variables and flags always win. A missing file is not an error.
func Load(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := parse(sc.Text())
		if !ok {
			continue
		}
		if _, set := os.LookupEnv(k); !set {
			_ = os.Setenv(k, v)
		}
	}
	return sc.Err()
}

func parse(line string) (key, val string, ok bool) {
	line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "\ufeff"))
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false
	}
	line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
	k, v, found := strings.Cut(line, "=")
	k = strings.TrimSpace(k)
	if !found || k == "" {
		return "", "", false
	}
	v = strings.TrimSpace(v)
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
		return k, v[1 : len(v)-1], true
	}
	if i := strings.Index(v, " #"); i >= 0 { // trailing comment on an unquoted value
		v = strings.TrimSpace(v[:i])
	}
	return k, v, true
}

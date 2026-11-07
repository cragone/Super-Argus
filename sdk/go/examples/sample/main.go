// Command sample is a stand-in application that logs through the Argus Go SDK
// at a steady rate and reports, once a second, how long its log calls took.
// sdk/e2e/run.mjs uses it to show that Argus going down does not affect an app.
//
//	ARGUS_URL=http://localhost:8080 ARGUS_KEY=... DURATION_S=20 RATE=2000 go run ./examples/sample
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strconv"
	"time"

	"github.com/cragone/Super-Argus/sdk/go/argus"
)

func envInt(k string, def int) int {
	if n, err := strconv.Atoi(os.Getenv(k)); err == nil && n > 0 {
		return n
	}
	return def
}

type report struct {
	TMS     int64 `json:"t_ms"`
	Calls   int   `json:"calls"`
	P99US   int64 `json:"p99_us"`
	MaxUS   int64 `json:"max_us"`
	Logged  int64 `json:"logged"`
	Sent    int64 `json:"sent"`
	Dropped int64 `json:"dropped"`
	Final   bool  `json:"final,omitempty"`
}

func main() {
	ship := argus.New(argus.Config{
		URL: os.Getenv("ARGUS_URL"), APIKey: os.Getenv("ARGUS_KEY"),
		Labels:     map[string]string{"service": os.Getenv("SERVICE_LABEL"), "sdk": "go"},
		FlushEvery: 200 * time.Millisecond, Backoff: 200 * time.Millisecond, MaxRetries: 2,
		Client: nil, OnError: func(string) {}, // the harness reads the counters; keep stdout clean
	})
	if ship == nil {
		fmt.Fprintln(os.Stderr, "set ARGUS_URL and ARGUS_KEY")
		os.Exit(2)
	}
	logger := slog.New(ship.Handler(slog.LevelInfo))
	dur := time.Duration(envInt("DURATION_S", 20)) * time.Second
	perTick := envInt("RATE", 2000) / 100

	out := json.NewEncoder(os.Stdout)
	var logged int64
	var window []time.Duration
	emit := func(final bool) {
		sort.Slice(window, func(i, j int) bool { return window[i] < window[j] })
		r := report{TMS: time.Now().UnixMilli(), Calls: len(window), Logged: logged, Final: final}
		if n := len(window); n > 0 {
			r.P99US = window[n*99/100].Microseconds()
			r.MaxUS = window[n-1].Microseconds()
		}
		st := ship.Stats()
		r.Sent, r.Dropped = st.Sent, st.Dropped
		_ = out.Encode(r)
		window = window[:0]
	}

	start := time.Now()
	tick := time.NewTicker(10 * time.Millisecond)
	report1s := time.NewTicker(time.Second)
	for time.Since(start) < dur {
		select {
		case <-tick.C:
			for i := 0; i < perTick; i++ {
				t0 := time.Now()
				logger.Info("request handled", "n", logged, "path", "/api/v1/things/"+strconv.Itoa(int(logged)%97))
				window = append(window, time.Since(t0))
				logged++
			}
		case <-report1s.C:
			emit(false)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	ship.Close(ctx)
	emit(true)
}

// Command traffic drives the test app with a realistic mix of requests so there is
// something to watch in Argus, and can create a ready-made "Test app" dashboard.
//
//	go run ./cmd/traffic -duration 2m -rps 15
//	go run ./cmd/traffic -dashboard -argus http://localhost:8080 -email admin@example.com -password ...
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"time"
)

type route struct {
	weight int
	method string
	// build returns the path and an optional JSON body.
	build func(r *rand.Rand) (path, body string)
}

var routes = []route{
	{30, "GET", func(r *rand.Rand) (string, string) {
		if r.IntN(4) == 0 {
			return "/items?q=" + []string{"anvil", "bolt", "cable", "gear"}[r.IntN(4)], ""
		}
		return "/items", ""
	}},
	{12, "POST", func(r *rand.Rand) (string, string) {
		return "/items", fmt.Sprintf(`{"name":"item-%d","qty":%d}`, r.IntN(10000), r.IntN(50))
	}},
	{4, "POST", func(r *rand.Rand) (string, string) { return "/items", `{"name":"","qty":-1}` }}, // 422
	{15, "GET", func(r *rand.Rand) (string, string) { return fmt.Sprintf("/items/%d", 1+r.IntN(400)), "" }},
	{3, "DELETE", func(r *rand.Rand) (string, string) { return fmt.Sprintf("/items/%d", 1+r.IntN(400)), "" }},
	{6, "GET", func(r *rand.Rand) (string, string) { return fmt.Sprintf("/slow?ms=%d", 100+r.IntN(1100)), "" }},
	{10, "GET", func(r *rand.Rand) (string, string) { return "/flaky?p=0.3", "" }},
	{2, "GET", func(r *rand.Rand) (string, string) { return "/error", "" }},
	{1, "GET", func(r *rand.Rand) (string, string) { return "/panic", "" }},
	{4, "GET", func(r *rand.Rand) (string, string) { return "/noise?n=5", "" }},
	{2, "POST", func(r *rand.Rand) (string, string) { return "/seed?n=10", "" }},
	{5, "GET", func(r *rand.Rand) (string, string) { return "/healthz", "" }},
}

// pick chooses a route by weight.
func pick(r *rand.Rand) route {
	total := 0
	for _, x := range routes {
		total += x.weight
	}
	n := r.IntN(total)
	for _, x := range routes {
		if n < x.weight {
			return x
		}
		n -= x.weight
	}
	return routes[0]
}

func main() {
	target := flag.String("url", "http://localhost:9090", "test app base URL")
	duration := flag.Duration("duration", time.Minute, "how long to run (0 = until Ctrl-C)")
	rps := flag.Float64("rps", 10, "requests per second")
	conc := flag.Int("concurrency", 4, "parallel workers")
	dash := flag.Bool("dashboard", false, "create the \"Test app\" dashboard in Argus and exit")
	argusURL := flag.String("argus", "http://localhost:8080", "Argus base URL (with -dashboard)")
	email := flag.String("email", os.Getenv("ARGUS_EMAIL"), "Argus admin/editor email (with -dashboard)")
	password := flag.String("password", os.Getenv("ARGUS_PASSWORD"), "Argus password (with -dashboard)")
	flag.Parse()

	if *dash {
		if *email == "" || *password == "" {
			log.Fatal("-dashboard needs -email and -password (or ARGUS_EMAIL / ARGUS_PASSWORD)")
		}
		id, created, err := SetupDashboard(context.Background(), http.DefaultClient, *argusURL, *email, *password)
		if err != nil {
			log.Fatalf("dashboard: %v", err)
		}
		state := "already exists"
		if created {
			state = "created"
		}
		fmt.Printf("dashboard %s (id %s). Open it in the Argus app: Dashboards tab -> Test app\n", state, id)
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if *duration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *duration)
		defer cancel()
	}
	run(ctx, *target, *rps, *conc)
}

type tally struct {
	mu     sync.Mutex
	counts map[string]int // "METHOD route-ish status" -> n
	errs   int
	total  int
}

func (t *tally) add(key string, err bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.total++
	if err {
		t.errs++
		return
	}
	t.counts[key]++
}

func run(ctx context.Context, target string, rps float64, conc int) {
	target = strings.TrimRight(target, "/")
	tl := &tally{counts: map[string]int{}}
	client := &http.Client{Timeout: 15 * time.Second}
	jobs := make(chan struct{}, conc*2)

	var wg sync.WaitGroup
	for i := 0; i < conc; i++ {
		wg.Add(1)
		go func(seed uint64) {
			defer wg.Done()
			rng := rand.New(rand.NewPCG(seed, seed*7919))
			for range jobs {
				rt := pick(rng)
				path, body := rt.build(rng)
				req, _ := http.NewRequestWithContext(ctx, rt.method, target+path, strings.NewReader(body))
				if body != "" {
					req.Header.Set("Content-Type", "application/json")
				}
				res, err := client.Do(req)
				if err != nil {
					if ctx.Err() == nil { // a request cut off by the end of the run is not a failure
						tl.add("", true)
					}
					continue
				}
				_, _ = io.Copy(io.Discard, res.Body)
				res.Body.Close()
				route := path
				if i := strings.IndexByte(route, '?'); i >= 0 {
					route = route[:i]
				}
				if strings.HasPrefix(route, "/items/") {
					route = "/items/:id"
				}
				tl.add(fmt.Sprintf("%-6s %-12s %d", rt.method, route, res.StatusCode), false)
			}
		}(uint64(i + 1))
	}

	fmt.Printf("sending ~%.0f req/s to %s (Ctrl-C to stop)\n", rps, target)
	tick := time.NewTicker(time.Duration(float64(time.Second) / rps))
	defer tick.Stop()
	report := time.NewTicker(10 * time.Second)
	defer report.Stop()
loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case <-report.C:
			tl.mu.Lock()
			fmt.Printf("  %d requests so far, %d failed to connect\n", tl.total, tl.errs)
			tl.mu.Unlock()
		case <-tick.C:
			select {
			case jobs <- struct{}{}:
			default: // workers are saturated; skip rather than queue up
			}
		}
	}
	close(jobs)
	wg.Wait()

	tl.mu.Lock()
	defer tl.mu.Unlock()
	keys := make([]string, 0, len(tl.counts))
	for k := range tl.counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Printf("\n%d requests, %d could not connect\n", tl.total, tl.errs)
	for _, k := range keys {
		fmt.Printf("  %-32s %5d\n", k, tl.counts[k])
	}
	if tl.errs > 0 && tl.errs == tl.total {
		fmt.Println("nothing connected: is the test app running? (go run . in testapp/)")
	}
}

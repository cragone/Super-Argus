package main

import (
	"bufio"
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// kube names use this alphabet for generated suffixes (no vowels, no 0/1/3).
const kubeAlnum = "bcdfghjklmnpqrstvwxz2456789"

var (
	deployPod  = regexp.MustCompile(`^(.+)-[` + kubeAlnum + `]{8,10}-[` + kubeAlnum + `]{5}$`)
	genSuffix  = regexp.MustCompile(`^(.+)-[` + kubeAlnum + `]{5}$`) // daemonset / job pods
	ordinalPod = regexp.MustCompile(`^(.+)-\d+$`)                    // statefulset pods
	logFile    = regexp.MustCompile(`^\d+\.log$`)
)

// serviceFromPod maps a pod name to the workload that owns it, so replicas and
// restarts share one service: api-7d9f8b6c5d-x2k4p -> api.
func serviceFromPod(pod string) string {
	for _, re := range []*regexp.Regexp{deployPod, genSuffix, ordinalPod} {
		if m := re.FindStringSubmatch(pod); m != nil {
			return m[1]
		}
	}
	return pod
}

// podDirLabels parses a /var/log/pods directory name "<namespace>_<pod>_<uid>".
func podDirLabels(dir, container string) (map[string]string, bool) {
	parts := strings.Split(dir, "_")
	if len(parts) != 3 {
		return nil, false
	}
	return map[string]string{
		"service":   serviceFromPod(parts[1]),
		"namespace": parts[0],
		"pod":       parts[1],
		"container": container,
		"source":    "kubernetes",
	}, true
}

// criLine is one parsed container-runtime log line.
type criLine struct {
	TS      time.Time
	Partial bool
	Text    string
}

// parseCRI parses "<rfc3339nano> <stdout|stderr> <F|P> <message>".
func parseCRI(line string) (criLine, bool) {
	ts, rest, ok := strings.Cut(line, " ")
	if !ok {
		return criLine{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return criLine{}, false
	}
	_, rest, ok = strings.Cut(rest, " ") // stream
	if !ok {
		return criLine{}, false
	}
	tag, msg, _ := strings.Cut(rest, " ")
	if tag != "F" && tag != "P" {
		return criLine{}, false
	}
	return criLine{TS: t, Partial: tag == "P", Text: msg}, true
}

const maxPartial = 64 << 10

// runKubernetes discovers pod container logs under root and tails them.
// New pods are found by rescanning, so nothing needs configuring per workload.
func runKubernetes(ctx context.Context, hub *Hub, st *State, root string, excludeNS map[string]bool) {
	var (
		mu      sync.Mutex
		tailing = map[string]bool{}
		wg      sync.WaitGroup
		first   = true
	)
	scan := func() {
		files, _ := filepath.Glob(filepath.Join(root, "*", "*", "*.log"))
		for _, path := range files {
			if !logFile.MatchString(filepath.Base(path)) {
				continue
			}
			container := filepath.Base(filepath.Dir(path))
			labels, ok := podDirLabels(filepath.Base(filepath.Dir(filepath.Dir(path))), container)
			if !ok || excludeNS[labels["namespace"]] {
				continue
			}
			mu.Lock()
			if tailing[path] {
				mu.Unlock()
				continue
			}
			tailing[path] = true
			mu.Unlock()

			// Files present when the agent first starts (with no saved position) are
			// followed from their end; files that appear later are read from the start.
			start := int64(0)
			if off, ok := st.Offset(path); ok {
				start = off
			} else if first {
				if fi, err := os.Stat(path); err == nil {
					start = fi.Size()
				}
			}
			wg.Add(1)
			go func(path string, labels map[string]string, start int64) {
				defer wg.Done()
				tailFile(ctx, hub, st, path, labels, start)
				mu.Lock()
				delete(tailing, path)
				mu.Unlock()
			}(path, labels, start)
		}
		first = false
	}
	scan()
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			return
		case <-tick.C:
			scan()
		}
	}
}

// tailFile follows one CRI log file from offset until it is deleted or ctx ends.
func tailFile(ctx context.Context, hub *Hub, st *State, path string, labels map[string]string, off int64) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		off = 0
		_, _ = f.Seek(0, io.SeekStart)
	}
	r := bufio.NewReaderSize(f, 64<<10)
	var pending []byte // bytes of a line whose newline has not arrived yet
	var partial strings.Builder
	var partialTS time.Time

	for ctx.Err() == nil {
		chunk, err := r.ReadBytes('\n')
		pending = append(pending, chunk...)
		if err == nil {
			off += int64(len(pending))
			text := strings.TrimRight(string(pending), "\r\n")
			pending = pending[:0]
			if c, ok := parseCRI(text); ok {
				if partial.Len() == 0 {
					partialTS = c.TS
				}
				if partial.Len()+len(c.Text) <= maxPartial {
					partial.WriteString(c.Text)
				}
				if !c.Partial {
					if partial.Len() > 0 {
						hub.Send(labels, partialTS, partial.String())
					}
					partial.Reset()
				}
			}
			st.SetOffset(path, off)
			continue
		}
		// EOF: wait for more, noticing deletion and truncation.
		select {
		case <-ctx.Done():
			return
		case <-time.After(500 * time.Millisecond):
		}
		fi, serr := os.Stat(path)
		if serr != nil {
			st.Forget(path)
			return
		}
		if fi.Size() < off+int64(len(pending)) { // truncated or replaced
			off, pending = 0, pending[:0]
			_, _ = f.Seek(0, io.SeekStart)
			r.Reset(f)
		}
	}
	if err := ctx.Err(); err != nil {
		log.Printf("argus-agent: stopped tailing %s", path)
	}
}

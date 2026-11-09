package perf

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"log"
	"math"
	"os"
	"path/filepath"
	"runtime/metrics"
	"strings"
	"testing"
	"time"
)

func readRecords(t *testing.T, path string) []map[string]any {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open perf log: %v", err)
	}
	defer f.Close()
	var out []map[string]any
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var rec map[string]any
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			t.Fatalf("invalid JSON line %q: %v", sc.Text(), err)
		}
		out = append(out, rec)
	}
	return out
}

func TestCollectorWritesStartAndShutdownRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "perf.log")
	c, err := New(Options{Path: path, Interval: time.Hour, DiskPath: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	c.AddSource("custom", func() map[string]any { return map[string]any{"answer": 42} })
	c.AddSource("broken", func() map[string]any { panic("boom") })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); c.Run(ctx) }()
	cancel()
	<-done

	recs := readRecords(t, path)
	if len(recs) != 2 {
		t.Fatalf("want 2 records (start, shutdown), got %d", len(recs))
	}
	if recs[0]["event"] != "start" || recs[1]["event"] != "shutdown" {
		t.Fatalf("unexpected events: %v / %v", recs[0]["event"], recs[1]["event"])
	}
	rt, ok := recs[0]["runtime"].(map[string]any)
	if !ok || rt["goroutines"].(float64) < 1 {
		t.Fatalf("runtime section missing goroutines: %v", recs[0]["runtime"])
	}
	for _, key := range []string{"heap_alloc_bytes", "num_gc", "sched_latency_p99_ms", "gomaxprocs"} {
		if _, ok := rt[key]; !ok {
			t.Errorf("runtime.%s missing", key)
		}
	}
	proc, ok := recs[0]["process"].(map[string]any)
	if !ok || proc["pid"].(float64) != float64(os.Getpid()) {
		t.Fatalf("process section wrong: %v", recs[0]["process"])
	}
	if got := recs[0]["custom"].(map[string]any)["answer"]; got != float64(42) {
		t.Fatalf("custom source not recorded: %v", recs[0]["custom"])
	}
	if got := recs[0]["broken"].(map[string]any)["error"]; got != "panic" {
		t.Fatalf("panicking source should be isolated, got %v", recs[0]["broken"])
	}
	if _, ok := recs[1]["process"].(map[string]any)["cpu_percent"]; !ok {
		t.Fatalf("second sample should carry cpu_percent: %v", recs[1]["process"])
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("perf log must not be group/world accessible: %v", info.Mode())
	}
}

func TestCollectorDoesNotLogSourcePayloads(t *testing.T) {
	var output bytes.Buffer
	original := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(original)

	const payload = "synthetic-sensitive-source-payload"
	result := safeSource("broken", func() map[string]any { panic(payload) })
	if result["error"] != "panic" {
		t.Fatal("panicking source must return a sanitized error")
	}

	c, err := New(Options{Path: filepath.Join(t.TempDir(), "perf.log")})
	if err != nil {
		t.Fatal(err)
	}
	defer c.file.Close()
	c.AddSource("invalid", func() map[string]any {
		return map[string]any{"metric": json.Number(payload)}
	})
	c.sample("test")
	if strings.Contains(output.String(), payload) {
		t.Fatal("diagnostic log leaked source payload")
	}
	if !strings.Contains(output.String(), "panicked") || !strings.Contains(output.String(), "sample encoding failed") {
		t.Fatal("sanitized diagnostic messages missing")
	}
	if records := readRecords(t, c.opts.Path); len(records) != 0 {
		t.Fatal("invalid sample must not reach the performance log")
	}
}

func TestCollectorRotatesAtMaxBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "perf.log")
	c, err := New(Options{Path: path, Interval: time.Hour, MaxBytes: 1})
	if err != nil {
		t.Fatal(err)
	}
	c.sample("one")
	c.sample("two")
	c.sample("three")
	if recs := readRecords(t, path); len(recs) != 1 || recs[0]["event"] != "three" {
		t.Fatalf("current file should hold only the newest record: %v", recs)
	}
	if recs := readRecords(t, path+".1"); len(recs) != 1 || recs[0]["event"] != "two" {
		t.Fatalf("rotated file should hold the previous record: %v", recs)
	}
}

func TestNewRejectsEmptyPathAndAppliesDefaults(t *testing.T) {
	if _, err := New(Options{}); err == nil {
		t.Fatal("empty path must be rejected")
	}
	c, err := New(Options{Path: filepath.Join(t.TempDir(), "p.log"), Interval: 10 * time.Millisecond, MaxBytes: -1})
	if err != nil {
		t.Fatal(err)
	}
	if c.opts.Interval != DefaultInterval || c.opts.MaxBytes != DefaultMaxBytes {
		t.Fatalf("defaults not applied: %+v", c.opts)
	}
}

func TestPercentile(t *testing.T) {
	h := &metrics.Float64Histogram{
		Counts:  []uint64{0, 90, 9, 1},
		Buckets: []float64{0, 0.001, 0.01, 0.1, math.Inf(1)},
	}
	if got := percentile(h, 0.5); got != 0.01 {
		t.Fatalf("p50 = %v, want 0.01", got)
	}
	if got := percentile(h, 0.99); got != 0.1 {
		t.Fatalf("p99 = %v, want 0.1", got)
	}
	if got := percentile(h, 1.0); got != 0.1 {
		t.Fatalf("max should fall back to finite bound, got %v", got)
	}
	if got := percentile(&metrics.Float64Histogram{Counts: []uint64{0}, Buckets: []float64{0, 1}}, 0.5); got != 0 {
		t.Fatalf("empty histogram should yield 0, got %v", got)
	}
}

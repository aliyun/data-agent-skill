// Package perf periodically samples host, process, Go runtime and MCP server
// metrics and appends them as JSON lines to a dedicated log file, so a
// production incident can be reconstructed after the fact.
package perf

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"runtime/metrics"
	"sync"
	"time"

	"github.com/alibabacloud/data-agent-mcp-server/internal/logging"
)

const (
	DefaultInterval = 30 * time.Second
	DefaultMaxBytes = 64 << 20
	minInterval     = time.Second
)

// Source returns one named section of a sample. It must be cheap and must not
// block: the collector calls every source on each tick from a single goroutine.
type Source func() map[string]any

// Options configure a Collector.
type Options struct {
	Path     string        // log file; parent directory is created if missing
	Interval time.Duration // sampling period, floored at one second
	MaxBytes int64         // bytes per file; current plus seven dated backups
	DiskPath string        // filesystem to report free space for (e.g. sessions dir)
}

// Collector owns the sample loop and the output file.
type Collector struct {
	opts    Options
	started time.Time

	mu      sync.Mutex
	sources []namedSource
	file    *os.File
	size    int64
	failed  bool

	prevProcCPU procCPU
	prevHostCPU hostCPU
	prevSample  time.Time
	prevPause   uint64
	prevNumGC   uint32
}

type namedSource struct {
	name string
	fn   Source
}

// New validates options and opens the log file. The first sample is written
// by Run; New only fails on an unusable path.
func New(opts Options) (*Collector, error) {
	if opts.Path == "" {
		return nil, errors.New("perf log path required")
	}
	if opts.Interval < minInterval {
		opts.Interval = DefaultInterval
	}
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = DefaultMaxBytes
	}
	if err := os.MkdirAll(filepath.Dir(opts.Path), 0o700); err != nil {
		return nil, fmt.Errorf("perf log directory: %w", err)
	}
	c := &Collector{opts: opts, started: time.Now()}
	if err := c.open(); err != nil {
		return nil, err
	}
	return c, nil
}

// AddSource registers a named section. Sections appear in the sample under
// their name; registration order is preserved in the output.
func (c *Collector) AddSource(name string, fn Source) {
	if fn == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sources = append(c.sources, namedSource{name: name, fn: fn})
}

// Run samples until ctx is cancelled, then writes a final sample tagged
// "shutdown" and closes the file.
func (c *Collector) Run(ctx context.Context) {
	c.sample("start")
	ticker := time.NewTicker(c.opts.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			c.sample("")
		case <-ctx.Done():
			c.sample("shutdown")
			c.mu.Lock()
			if c.file != nil {
				_ = c.file.Close()
				c.file = nil
			}
			c.mu.Unlock()
			return
		}
	}
}

// Sample collects one record without writing it; exposed for tests and for
// on-demand dumps.
func (c *Collector) Sample() map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.collectLocked("")
}

func (c *Collector) sample(event string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	rec := c.collectLocked(event)
	data, err := json.Marshal(rec)
	if err != nil {
		log.Print("perf: sample encoding failed")
		return
	}
	data = append(data, '\n')
	if c.file == nil {
		if err := c.open(); err != nil {
			c.writeFailed()
			return
		}
	}
	if c.size > 0 && int64(len(data)) > c.opts.MaxBytes-c.size {
		if err := c.rotateLocked(); err != nil {
			c.writeFailed()
			return
		}
	}
	n, err := c.file.Write(data)
	c.size += int64(n)
	if err != nil || n != len(data) {
		_ = c.file.Close()
		c.file = nil
		c.writeFailed()
		return
	}
	if c.failed {
		log.Print("perf: file output recovered; samples during failure were dropped")
		c.failed = false
	}
}

func (c *Collector) collectLocked(event string) map[string]any {
	now := time.Now()
	rec := map[string]any{
		"ts":       now.Format(time.RFC3339Nano),
		"uptime_s": int64(now.Sub(c.started).Seconds()),
	}
	if event != "" {
		rec["event"] = event
	}
	elapsed := time.Duration(0)
	if !c.prevSample.IsZero() {
		elapsed = now.Sub(c.prevSample)
	}
	rec["runtime"] = c.runtimeMetrics()
	rec["process"] = c.processMetrics(elapsed)
	if host := c.hostMetrics(); len(host) > 0 {
		rec["host"] = host
	}
	for _, s := range c.sources {
		rec[s.name] = safeSource(s.name, s.fn)
	}
	c.prevSample = now
	return rec
}

func safeSource(name string, fn Source) (out map[string]any) {
	defer func() {
		if recover() != nil {
			log.Printf("perf: source %q panicked", name)
			out = map[string]any{"error": "panic"}
		}
	}()
	return fn()
}

func (c *Collector) open() error {
	f, err := os.OpenFile(c.opts.Path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("perf log open: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return fmt.Errorf("perf log stat: %w", err)
	}
	c.file, c.size = f, info.Size()
	return nil
}

// Rotate and prune under the same lock as writes.
func (c *Collector) rotateLocked() error {
	err := c.file.Close()
	c.file = nil
	if err != nil {
		return err
	}
	if err := logging.Rotate(c.opts.Path); err != nil {
		return err
	}
	return c.open()
}

func (c *Collector) writeFailed() {
	if !c.failed {
		log.Print("perf: file output unavailable; retrying on the next sample")
		c.failed = true
	}
}

// runtimeMetrics reports Go scheduler and heap state. The GC pause and
// scheduling-latency values are the strongest signals of a stalled server.
func (c *Collector) runtimeMetrics() map[string]any {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	out := map[string]any{
		"goroutines":        runtime.NumGoroutine(),
		"gomaxprocs":        runtime.GOMAXPROCS(0),
		"num_cpu":           runtime.NumCPU(),
		"heap_alloc_bytes":  ms.HeapAlloc,
		"heap_inuse_bytes":  ms.HeapInuse,
		"heap_idle_bytes":   ms.HeapIdle,
		"heap_sys_bytes":    ms.HeapSys,
		"heap_objects":      ms.HeapObjects,
		"stack_inuse_bytes": ms.StackInuse,
		"sys_bytes":         ms.Sys,
		"mallocs_total":     ms.Mallocs,
		"frees_total":       ms.Frees,
		"num_gc":            ms.NumGC,
		"gc_total_pause_ms": float64(ms.PauseTotalNs) / 1e6,
		"next_gc_bytes":     ms.NextGC,
		"gc_cpu_fraction":   ms.GCCPUFraction,
	}
	if ms.NumGC > c.prevNumGC {
		out["gc_count_delta"] = ms.NumGC - c.prevNumGC
		out["gc_pause_delta_ms"] = float64(ms.PauseTotalNs-c.prevPause) / 1e6
		out["gc_last_pause_ms"] = float64(ms.PauseNs[(ms.NumGC+255)%256]) / 1e6
	}
	c.prevNumGC, c.prevPause = ms.NumGC, ms.PauseTotalNs
	if ms.LastGC > 0 {
		out["gc_last_age_s"] = time.Since(time.Unix(0, int64(ms.LastGC))).Seconds()
	}
	for k, v := range schedMetrics() {
		out[k] = v
	}
	return out
}

var schedSamples = []metrics.Sample{
	{Name: "/sched/latencies:seconds"},
	{Name: "/sched/goroutines:goroutines"},
	{Name: "/sync/mutex/wait/total:seconds"},
	{Name: "/cpu/classes/gc/total:cpu-seconds"},
	{Name: "/cpu/classes/user:cpu-seconds"},
	{Name: "/gc/pauses:seconds"},
}

// schedMetrics reads the runtime/metrics histograms that MemStats lacks:
// scheduling latency (how long runnable goroutines waited for a P) and
// cumulative mutex wait.
func schedMetrics() map[string]any {
	samples := make([]metrics.Sample, len(schedSamples))
	copy(samples, schedSamples)
	metrics.Read(samples)
	out := map[string]any{}
	for _, s := range samples {
		switch s.Value.Kind() {
		case metrics.KindFloat64:
			switch s.Name {
			case "/sync/mutex/wait/total:seconds":
				out["mutex_wait_total_s"] = s.Value.Float64()
			case "/cpu/classes/gc/total:cpu-seconds":
				out["gc_cpu_total_s"] = s.Value.Float64()
			case "/cpu/classes/user:cpu-seconds":
				out["user_cpu_total_s"] = s.Value.Float64()
			}
		case metrics.KindFloat64Histogram:
			h := s.Value.Float64Histogram()
			switch s.Name {
			case "/sched/latencies:seconds":
				out["sched_latency_p50_ms"] = percentile(h, 0.50) * 1e3
				out["sched_latency_p99_ms"] = percentile(h, 0.99) * 1e3
				out["sched_latency_max_ms"] = percentile(h, 1.0) * 1e3
			case "/gc/pauses:seconds":
				out["gc_pause_p99_ms"] = percentile(h, 0.99) * 1e3
			}
		}
	}
	return out
}

// percentile returns the upper bucket bound at the requested quantile of a
// cumulative runtime histogram. Buckets are +Inf-terminated; the finite upper
// neighbour is returned for the last bucket so the value stays plottable.
func percentile(h *metrics.Float64Histogram, q float64) float64 {
	var total uint64
	for _, n := range h.Counts {
		total += n
	}
	if total == 0 {
		return 0
	}
	target := uint64(float64(total) * q)
	if target == 0 {
		target = 1
	}
	var cum uint64
	for i, n := range h.Counts {
		cum += n
		if cum >= target {
			upper := h.Buckets[i+1]
			if math.IsInf(upper, 1) || math.IsNaN(upper) {
				return h.Buckets[i]
			}
			return upper
		}
	}
	return h.Buckets[len(h.Buckets)-1]
}

// processMetrics reports CPU and memory of this process as seen by the OS.
func (c *Collector) processMetrics(elapsed time.Duration) map[string]any {
	out := map[string]any{"pid": os.Getpid()}
	cpu, ok := readProcCPU()
	if ok {
		out["cpu_user_total_s"] = cpu.user.Seconds()
		out["cpu_sys_total_s"] = cpu.sys.Seconds()
		if elapsed > 0 && c.prevProcCPU.valid {
			busy := (cpu.user - c.prevProcCPU.user) + (cpu.sys - c.prevProcCPU.sys)
			out["cpu_percent"] = round2(100 * busy.Seconds() / elapsed.Seconds())
		}
		cpu.valid = true
		c.prevProcCPU = cpu
	}
	for k, v := range readProcStatus() {
		out[k] = v
	}
	return out
}

// hostMetrics reports machine-wide load, CPU and memory where the platform
// exposes them (Linux /proc). Other platforms return nil.
func (c *Collector) hostMetrics() map[string]any {
	out := readHostStatic()
	cpu, ok := readHostCPU()
	if ok {
		if c.prevHostCPU.valid && cpu.total > c.prevHostCPU.total {
			dt := cpu.total - c.prevHostCPU.total
			out["cpu_percent"] = round2(100 * float64(dt-(cpu.idle-c.prevHostCPU.idle)) / float64(dt))
			out["cpu_iowait_percent"] = round2(100 * float64(cpu.iowait-c.prevHostCPU.iowait) / float64(dt))
			out["cpu_steal_percent"] = round2(100 * float64(cpu.steal-c.prevHostCPU.steal) / float64(dt))
		}
		cpu.valid = true
		c.prevHostCPU = cpu
	}
	if c.opts.DiskPath != "" {
		if free, total, ok := diskUsage(c.opts.DiskPath); ok {
			out["disk_free_bytes"] = free
			out["disk_total_bytes"] = total
		}
	}
	return out
}

type procCPU struct {
	user, sys time.Duration
	valid     bool
}

type hostCPU struct {
	total, idle, iowait, steal uint64
	valid                      bool
}

func round2(v float64) float64 {
	return float64(int64(v*100+0.5)) / 100
}

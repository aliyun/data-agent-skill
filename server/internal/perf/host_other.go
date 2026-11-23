//go:build !linux

package perf

// Host-level and /proc-derived process metrics are only read on Linux, the
// deployment platform; other platforms still get runtime and rusage data.

func readProcStatus() map[string]any { return nil }

func readHostCPU() (hostCPU, bool) { return hostCPU{}, false }

func readHostStatic() map[string]any { return map[string]any{} }

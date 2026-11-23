//go:build !unix

package perf

func readProcCPU() (procCPU, bool) { return procCPU{}, false }

func diskUsage(string) (free, total uint64, ok bool) { return 0, 0, false }

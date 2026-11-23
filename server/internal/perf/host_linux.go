//go:build linux

package perf

import (
	"bufio"
	"bytes"
	"os"
	"strconv"
	"strings"
)

// readProcStatus reports RSS, thread and fd counts from /proc/self.
func readProcStatus() map[string]any {
	out := map[string]any{}
	if data, err := os.ReadFile("/proc/self/status"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			key, value, ok := strings.Cut(line, ":")
			if !ok {
				continue
			}
			value = strings.TrimSpace(value)
			switch key {
			case "VmRSS":
				out["rss_bytes"] = kbField(value)
			case "VmHWM":
				out["rss_peak_bytes"] = kbField(value)
			case "VmSwap":
				out["swap_bytes"] = kbField(value)
			case "Threads":
				if n, err := strconv.Atoi(value); err == nil {
					out["threads"] = n
				}
			case "voluntary_ctxt_switches":
				if n, err := strconv.ParseUint(value, 10, 64); err == nil {
					out["ctx_switches_voluntary"] = n
				}
			case "nonvoluntary_ctxt_switches":
				if n, err := strconv.ParseUint(value, 10, 64); err == nil {
					out["ctx_switches_involuntary"] = n
				}
			}
		}
	}
	if entries, err := os.ReadDir("/proc/self/fd"); err == nil {
		out["open_fds"] = len(entries)
	}
	if data, err := os.ReadFile("/proc/self/limits"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "Max open files") {
				fields := strings.Fields(strings.TrimPrefix(line, "Max open files"))
				if len(fields) > 0 {
					if n, err := strconv.Atoi(fields[0]); err == nil {
						out["max_fds"] = n
					}
				}
			}
		}
	}
	return out
}

func kbField(v string) uint64 {
	fields := strings.Fields(v)
	if len(fields) == 0 {
		return 0
	}
	n, _ := strconv.ParseUint(fields[0], 10, 64)
	return n * 1024
}

// readHostCPU parses the aggregate cpu line of /proc/stat.
func readHostCPU() (hostCPU, bool) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return hostCPU{}, false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 8 || fields[0] != "cpu" {
			continue
		}
		var vals [10]uint64
		for i := 1; i < len(fields) && i <= 10; i++ {
			vals[i-1], _ = strconv.ParseUint(fields[i], 10, 64)
		}
		var total uint64
		for _, v := range vals {
			total += v
		}
		// user nice system idle iowait irq softirq steal
		return hostCPU{total: total, idle: vals[3] + vals[4], iowait: vals[4], steal: vals[7]}, true
	}
	return hostCPU{}, false
}

// readHostStatic reports load average, memory and the root filesystem.
func readHostStatic() map[string]any {
	out := map[string]any{}
	if data, err := os.ReadFile("/proc/loadavg"); err == nil {
		fields := strings.Fields(string(data))
		if len(fields) >= 4 {
			out["load1"], _ = strconv.ParseFloat(fields[0], 64)
			out["load5"], _ = strconv.ParseFloat(fields[1], 64)
			out["load15"], _ = strconv.ParseFloat(fields[2], 64)
			if running, _, ok := strings.Cut(fields[3], "/"); ok {
				out["runnable_tasks"], _ = strconv.Atoi(running)
			}
		}
	}
	if data, err := os.ReadFile("/proc/meminfo"); err == nil {
		var total, available, swapTotal, swapFree uint64
		for _, line := range bytes.Split(data, []byte{'\n'}) {
			key, value, ok := strings.Cut(string(line), ":")
			if !ok {
				continue
			}
			switch key {
			case "MemTotal":
				total = kbField(value)
			case "MemAvailable":
				available = kbField(value)
			case "SwapTotal":
				swapTotal = kbField(value)
			case "SwapFree":
				swapFree = kbField(value)
			}
		}
		if total > 0 {
			out["mem_total_bytes"] = total
			out["mem_available_bytes"] = available
			out["mem_used_percent"] = round2(100 * float64(total-available) / float64(total))
		}
		if swapTotal > 0 {
			out["swap_used_bytes"] = swapTotal - swapFree
		}
	}
	if data, err := os.ReadFile("/proc/pressure/cpu"); err == nil {
		if v, ok := psiAvg10(data); ok {
			out["psi_cpu_some_avg10"] = v
		}
	}
	if data, err := os.ReadFile("/proc/pressure/memory"); err == nil {
		if v, ok := psiAvg10(data); ok {
			out["psi_mem_some_avg10"] = v
		}
	}
	if data, err := os.ReadFile("/proc/pressure/io"); err == nil {
		if v, ok := psiAvg10(data); ok {
			out["psi_io_some_avg10"] = v
		}
	}
	return out
}

// psiAvg10 extracts the "some avg10" value from a /proc/pressure file.
func psiAvg10(data []byte) (float64, bool) {
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "some ") {
			continue
		}
		for _, field := range strings.Fields(line) {
			if v, ok := strings.CutPrefix(field, "avg10="); ok {
				f, err := strconv.ParseFloat(v, 64)
				return f, err == nil
			}
		}
	}
	return 0, false
}

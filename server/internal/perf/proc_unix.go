//go:build unix

package perf

import (
	"syscall"
	"time"
)

func readProcCPU() (procCPU, bool) {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return procCPU{}, false
	}
	return procCPU{
		user: time.Duration(ru.Utime.Sec)*time.Second + time.Duration(ru.Utime.Usec)*time.Microsecond,
		sys:  time.Duration(ru.Stime.Sec)*time.Second + time.Duration(ru.Stime.Usec)*time.Microsecond,
	}, true
}

// diskUsage reports free and total bytes of the filesystem holding path.
func diskUsage(path string) (free, total uint64, ok bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, false
	}
	return st.Bavail * uint64(st.Bsize), st.Blocks * uint64(st.Bsize), true
}

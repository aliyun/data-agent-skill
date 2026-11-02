//go:build !windows

package tenant

import (
	"os"
	"syscall"
)

func storageNonblock() int { return syscall.O_NONBLOCK }
func trustedStorageParent(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || (int(st.Uid) != os.Geteuid() && st.Uid != 0) {
		return false
	}
	return info.Mode().Perm()&0022 == 0 || st.Uid == 0 && info.Mode()&os.ModeSticky != 0
}
func safeStorageOwner(info os.FileInfo, file bool) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) != os.Geteuid() {
		return false
	}
	if file {
		return info.Mode().IsRegular() && info.Mode().Perm()&0177 == 0
	}
	return info.IsDir() && info.Mode().Perm()&0022 == 0
}

//go:build !windows

package config

import (
	"os"
	"syscall"
)

func nonblockFlag() int { return syscall.O_NONBLOCK }
func privateSecretFile(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Geteuid() && info.Mode().IsRegular() && info.Mode().Perm()&0077 == 0 && info.Mode().Perm()&0111 == 0
}
func trustedSecretParent(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || (int(st.Uid) != os.Geteuid() && st.Uid != 0) {
		return false
	}
	return info.Mode().Perm()&0022 == 0 || (st.Uid == 0 && info.Mode()&os.ModeSticky != 0)
}

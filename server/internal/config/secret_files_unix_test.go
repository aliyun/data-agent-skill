//go:build !windows

package config

import (
	"os"
	"syscall"
	"testing"
	"time"
)

type syntheticFileInfo struct {
	mode os.FileMode
	uid  uint32
}

func (i syntheticFileInfo) Name() string       { return "synthetic" }
func (i syntheticFileInfo) Size() int64        { return 0 }
func (i syntheticFileInfo) Mode() os.FileMode  { return i.mode }
func (i syntheticFileInfo) ModTime() time.Time { return time.Time{} }
func (i syntheticFileInfo) IsDir() bool        { return i.mode.IsDir() }
func (i syntheticFileInfo) Sys() any           { return &syscall.Stat_t{Uid: i.uid} }

func TestSecretFileOwnerChecksWithoutChangingOwnership(t *testing.T) {
	uid := uint32(os.Geteuid())
	for _, mode := range []os.FileMode{0600, 0400} {
		if !privateSecretFile(syntheticFileInfo{mode: mode, uid: uid}) {
			t.Fatal("private owned file rejected")
		}
		if privateSecretFile(syntheticFileInfo{mode: mode, uid: uid + 1}) {
			t.Fatal("different owner accepted")
		}
	}
	for _, mode := range []os.FileMode{0644, 0620, 0601, 0700, os.ModeNamedPipe | 0600} {
		if privateSecretFile(syntheticFileInfo{mode: mode, uid: uid}) {
			t.Fatal("unsafe synthetic file accepted")
		}
	}
}

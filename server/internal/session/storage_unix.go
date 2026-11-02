//go:build unix

package session

import "syscall"

// Refuse final symlinks at open time and never block if a regular file is
// replaced by a FIFO between Lstat and OpenFile. The descriptor is still
// checked for regular type and identity before any bytes are read.
const regularReadFlags = syscall.O_NOFOLLOW | syscall.O_NONBLOCK

package logging

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

type Writer struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	size     int64
	file     *os.File
	fallback io.Writer
	closed   bool
}

func Open(path string, maxBytes int64) (*Writer, error) {
	if path == "" || maxBytes <= 0 {
		return nil, errors.New("MCP log file and positive size limit required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create MCP log directory: %w", err)
	}
	w := &Writer{path: path, maxBytes: maxBytes, fallback: os.Stderr}
	if err := w.open(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return 0, os.ErrClosed
	}
	if len(p) == 0 {
		return 0, nil
	}
	if w.file == nil {
		if err := w.open(); err != nil {
			return w.writeFallback(p)
		}
	}
	// A single oversized record stays intact rather than being split between files.
	if w.size > 0 && int64(len(p)) > w.maxBytes-w.size {
		if err := w.rotate(); err != nil {
			return w.writeFallback(p)
		}
	}
	n, err := w.file.Write(p)
	w.size += int64(n)
	if err != nil || n != len(p) {
		return w.writeFallback(p)
	}
	return n, nil
}

func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}

func logFileInfo(path string) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("MCP log must be a regular file, not a symlink")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("MCP log file must not be group- or world-accessible")
	}
	return info, nil
}

func (w *Writer) open() error {
	before, err := logFileInfo(w.path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open MCP log: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	after, err := logFileInfo(w.path)
	if err != nil {
		_ = f.Close()
		return err
	}
	if !os.SameFile(info, after) || (before != nil && !os.SameFile(info, before)) {
		_ = f.Close()
		return errors.New("MCP log file changed while opening")
	}
	w.file, w.size = f, info.Size()
	return nil
}

func (w *Writer) rotate() error {
	current, err := logFileInfo(w.path)
	if err != nil {
		return err
	}
	opened, err := w.file.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(current, opened) {
		return errors.New("MCP log file changed before rotation")
	}
	err = w.file.Close()
	w.file = nil
	if err != nil {
		return err
	}
	if err := os.Rename(w.path, w.path+".1"); err != nil {
		return fmt.Errorf("rotate MCP log: %w", err)
	}
	return w.open()
}

func (w *Writer) writeFallback(p []byte) (int, error) {
	if w.file != nil {
		_ = w.file.Close()
		w.file = nil
	}
	// The standard logger discards Write errors; preserve diagnostics on stderr.
	_, _ = fmt.Fprintln(w.fallback, "MCP log file unavailable; writing to stderr")
	return w.fallback.Write(p)
}

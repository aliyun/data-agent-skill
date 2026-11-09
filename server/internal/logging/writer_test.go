package logging

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func openWriter(t *testing.T, path string, maxBytes int64) *Writer {
	t.Helper()
	w, err := Open(path, maxBytes)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := w.Close(); err != nil {
			t.Error(err)
		}
	})
	return w
}

func assertFile(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != want {
		t.Fatalf("file content = %q, want %q", data, want)
	}
}

func writeRecord(t *testing.T, w *Writer, record string) {
	t.Helper()
	n, err := w.Write([]byte(record))
	if err != nil || n != len(record) {
		t.Fatalf("Write returned %d, %v", n, err)
	}
}

func TestAppendAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "server.log")
	w := openWriter(t, path, 8)
	writeRecord(t, w, "one\n")
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	w = openWriter(t, path, 8)
	writeRecord(t, w, "two\n")
	assertFile(t, path, "one\ntwo\n")
	writeRecord(t, w, "three\n")
	assertFile(t, path+".1", "one\ntwo\n")
	assertFile(t, path, "three\n")
	if runtime.GOOS != "windows" {
		for _, name := range []string{path, path + ".1", filepath.Dir(path)} {
			info, err := os.Stat(name)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm()&0o077 != 0 {
				t.Fatalf("log file/directory accessible to other users: %v", info.Mode())
			}
		}
	}
}

func TestRotationKeepsOneBackupAndWholeRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.log")
	w := openWriter(t, path, 8)
	writeRecord(t, w, "one\n")
	writeRecord(t, w, "two\n")
	if _, err := os.Stat(path + ".1"); !os.IsNotExist(err) {
		t.Fatalf("unexpected rotation at exactly max_bytes: %v", err)
	}
	writeRecord(t, w, "three\n")
	assertFile(t, path+".1", "one\ntwo\n")
	writeRecord(t, w, "four\n")
	assertFile(t, path+".1", "three\n")
	assertFile(t, path, "four\n")
	if _, err := os.Stat(path + ".2"); !os.IsNotExist(err) {
		t.Fatalf("unexpected second backup: %v", err)
	}
}

func TestOversizedRecordIsNotTruncated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.log")
	w := openWriter(t, path, 4)
	writeRecord(t, w, "oversized\n")
	assertFile(t, path, "oversized\n")
	if _, err := os.Stat(path + ".1"); !os.IsNotExist(err) {
		t.Fatalf("must not rotate an empty file: %v", err)
	}
	writeRecord(t, w, "end\n")
	assertFile(t, path+".1", "oversized\n")
	assertFile(t, path, "end\n")
}

func TestConcurrentWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.log")
	w := openWriter(t, path, 1<<20)
	var wg sync.WaitGroup
	for worker := 0; worker < 16; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				record := fmt.Sprintf("worker-%d-record-%d\n", worker, i)
				if n, err := w.Write([]byte(record)); err != nil || n != len(record) {
					t.Errorf("Write returned %d, %v", n, err)
				}
			}
		}()
	}
	wg.Wait()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	seen := make(map[string]bool)
	for _, line := range lines {
		seen[line] = true
	}
	if len(lines) != 800 || len(seen) != 800 {
		t.Fatalf("writes lost/interleaved: %d lines, %d unique", len(lines), len(seen))
	}
	for worker := 0; worker < 16; worker++ {
		for i := 0; i < 50; i++ {
			if !seen[fmt.Sprintf("worker-%d-record-%d", worker, i)] {
				t.Fatal("missing record")
			}
		}
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	w := openWriter(t, filepath.Join(t.TempDir(), "server.log"), 1024)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if n, err := w.Write([]byte("closed")); n != 0 || !errors.Is(err, os.ErrClosed) {
		t.Fatalf("write after close = %d, %v", n, err)
	}
}

func TestRotationFailureFallsBackAndRecovers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.log")
	w := openWriter(t, path, 4)
	var fallback bytes.Buffer
	w.fallback = &fallback
	writeRecord(t, w, "one\n")
	if err := os.Mkdir(path+".1", 0o700); err != nil {
		t.Fatal(err)
	}
	writeRecord(t, w, "two\n")
	assertFile(t, path, "one\n")
	if !strings.Contains(fallback.String(), "writing to stderr\ntwo\n") {
		t.Fatal("failed rotation silently lost the record")
	}
	if err := os.Remove(path + ".1"); err != nil {
		t.Fatal(err)
	}
	writeRecord(t, w, "end\n")
	assertFile(t, path+".1", "one\n")
	assertFile(t, path, "end\n")
}

func TestWriteFailurePreservesStandardLogRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.log")
	w := openWriter(t, path, 1024)
	var fallback bytes.Buffer
	w.fallback = &fallback
	if err := w.file.Close(); err != nil {
		t.Fatal(err)
	}
	logger := log.New(w, "", 0)
	logger.Print("record during file failure")
	if !strings.Contains(fallback.String(), "record during file failure\n") {
		t.Fatal("standard logger lost the record")
	}
	logger.Print("record after recovery")
	assertFile(t, path, "record after recovery\n")
}

func TestOpenRejectsInvalidOptions(t *testing.T) {
	for _, tc := range []struct {
		path string
		size int64
	}{
		{"", 1},
		{filepath.Join(t.TempDir(), "zero.log"), 0},
		{filepath.Join(t.TempDir(), "negative.log"), -1},
		{t.TempDir(), 1024},
	} {
		w, err := Open(tc.path, tc.size)
		if err == nil {
			_ = w.Close()
			t.Fatal("invalid log options accepted")
		}
	}
}

func TestOpenRejectsSymlinksAndAccessibleFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permissions and symlink creation")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "server.log")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if w, err := Open(link, 1024); err == nil {
		_ = w.Close()
		t.Fatal("symlink log file accepted")
	}
	assertFile(t, target, "unchanged")
	if err := os.Chmod(target, 0o644); err != nil {
		t.Fatal(err)
	}
	if w, err := Open(target, 1024); err == nil {
		_ = w.Close()
		t.Fatal("group/world-accessible log file accepted")
	}
	info, err := os.Stat(target)
	if err != nil || info.Mode().Perm() != 0o644 {
		t.Fatal("existing permissions must not be changed")
	}
	assertFile(t, target, "unchanged")
}

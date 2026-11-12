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
	"time"
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

func datedBackups(t *testing.T, path string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		if IsBackup(filepath.Base(path), entry.Name()) {
			names = append(names, filepath.Join(filepath.Dir(path), entry.Name()))
		}
	}
	return names
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
	backups := datedBackups(t, path)
	if len(backups) != 1 {
		t.Fatal("expected one dated backup", backups)
	}
	assertFile(t, backups[0], "one\ntwo\n")
	assertFile(t, path, "three\n")
	if runtime.GOOS != "windows" {
		for _, name := range []string{path, backups[0], filepath.Dir(path)} {
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

func TestRotationKeepsDatedBackupsAndWholeRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.log")
	w := openWriter(t, path, 8)
	writeRecord(t, w, "one\n")
	writeRecord(t, w, "two\n")
	if got := datedBackups(t, path); len(got) != 0 {
		t.Fatal("unexpected rotation at exactly max_bytes", got)
	}
	writeRecord(t, w, "three\n")
	writeRecord(t, w, "four\n")
	backups := datedBackups(t, path)
	if len(backups) != 2 {
		t.Fatal("same-day rotation lost a backup", backups)
	}
	assertFile(t, backups[0], "one\ntwo\n")
	assertFile(t, backups[1], "three\n")
	assertFile(t, path, "four\n")
	for _, name := range backups {
		if _, err := time.Parse(backupTimeFormat, strings.TrimPrefix(name, path+".")); err != nil {
			t.Fatal("backup suffix must be a UTC timestamp", name)
		}
	}
}

func TestRetentionKeepsEightFilesAndPrunesOldest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.log")
	w := openWriter(t, path, 16)
	unrelated := path + ".notes"
	if err := os.WriteFile(unrelated, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 20; i++ {
		writeRecord(t, w, fmt.Sprintf("record-%02d\n", i))
		if len(datedBackups(t, path)) > 7 {
			t.Fatal("more than eight log files retained")
		}
	}
	backups := datedBackups(t, path)
	if len(backups) != 7 {
		t.Fatal("expected seven backups", backups)
	}
	for i, name := range backups {
		assertFile(t, name, fmt.Sprintf("record-%02d\n", i+13))
	}
	assertFile(t, path, "record-20\n")
	assertFile(t, unrelated, "keep")
}

func TestRetentionIncludesLegacyNumberedBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.log")
	legacy := path + ".1"
	if err := os.WriteFile(legacy, []byte("legacy"), 0600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(legacy, old, old); err != nil {
		t.Fatal(err)
	}
	w := openWriter(t, path, 4)
	for i := 1; i <= 9; i++ {
		writeRecord(t, w, fmt.Sprintf("%03d\n", i))
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatal("legacy backup escaped retention")
	}
	if len(datedBackups(t, path)) != 7 {
		t.Fatal("legacy migration changed file limit")
	}
}

func TestFailedArchiveKeepsExistingBackups(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("requires POSIX directory permissions for a non-root process")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "server.log")
	w := openWriter(t, path, 4)
	var fallback bytes.Buffer
	w.fallback = &fallback
	for i := 1; i <= 8; i++ {
		writeRecord(t, w, fmt.Sprintf("%03d\n", i))
	}
	backups := datedBackups(t, path)
	if len(backups) != 7 {
		t.Fatal("expected full backup ring", backups)
	}
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0700)
	writeRecord(t, w, "009\n")
	for i, name := range backups {
		assertFile(t, name, fmt.Sprintf("%03d\n", i+1))
	}
	assertFile(t, path, "008\n")
	if len(datedBackups(t, path)) != 7 || !strings.Contains(fallback.String(), "009\n") {
		t.Fatal("failed archival deleted history or lost fallback")
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	writeRecord(t, w, "010\n")
	if len(datedBackups(t, path)) != 7 {
		t.Fatal("retention did not recover")
	}
	assertFile(t, path, "010\n")
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
	backups := datedBackups(t, path)
	if len(backups) != 1 {
		t.Fatal("missing oversized dated backup", backups)
	}
	assertFile(t, backups[0], "oversized\n")
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
	backups := datedBackups(t, path)
	if len(backups) != 1 {
		t.Fatal("missing dated backup after recovery", backups)
	}
	assertFile(t, backups[0], "one\n")
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

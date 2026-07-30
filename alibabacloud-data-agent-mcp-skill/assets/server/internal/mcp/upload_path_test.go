package mcp

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// newUploadServer builds a bare Server for path-policy tests; the upload
// checks depend only on the roots and the transport kind.
func newUploadServer(t *testing.T, remote bool, roots []string) *Server {
	t.Helper()
	s := &Server{remoteCaller: remote}
	s.SetUploadRoots(roots)
	return s
}

func TestResolveUploadPathAllowsFileInsideRoot(t *testing.T) {
	root := t.TempDir()
	want := filepath.Join(root, "data.csv")
	if err := os.WriteFile(want, []byte("a,b\n1,2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "sub")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	nestedFile := filepath.Join(nested, "more.csv")
	if err := os.WriteFile(nestedFile, []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	s := newUploadServer(t, true, []string{root})
	for _, p := range []string{want, nestedFile} {
		got, info, err := s.resolveUploadPath(p)
		if err != nil {
			t.Fatalf("resolveUploadPath(%s): unexpected error: %v", p, err)
		}
		if info == nil || info.Name() != filepath.Base(p) {
			t.Fatalf("resolveUploadPath(%s): unexpected info %v", p, info)
		}
		// t.TempDir may sit behind a symlink (/var -> /private/var on macOS),
		// so compare against the resolved form.
		realWant, err := filepath.EvalSymlinks(p)
		if err != nil {
			t.Fatal(err)
		}
		if got != realWant {
			t.Fatalf("resolveUploadPath(%s) = %s, want %s", p, got, realWant)
		}
	}
}

func TestResolveUploadPathRejectsOutsideRoot(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.env")
	if err := os.WriteFile(outside, []byte("AK=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	s := newUploadServer(t, true, []string{root})

	// Direct path outside the allowlist.
	if _, _, err := s.resolveUploadPath(outside); err == nil {
		t.Fatal("expected error for path outside the allowed directories")
	}
	// Traversal that escapes the root.
	traversal := filepath.Join(root, "..", filepath.Base(filepath.Dir(outside)), "secret.env")
	if _, _, err := s.resolveUploadPath(traversal); err == nil {
		t.Fatal("expected error for traversal outside the allowed directories")
	}
	// A sibling directory sharing the root's name prefix must not pass.
	sibling := root + "-evil"
	if err := os.Mkdir(sibling, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sibling) })
	siblingFile := filepath.Join(sibling, "data.csv")
	if err := os.WriteFile(siblingFile, []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.resolveUploadPath(siblingFile); err == nil {
		t.Fatal("expected error for sibling directory sharing the root prefix")
	}
}

func TestResolveUploadPathRejectsSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on Windows")
	}
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.env")
	if err := os.WriteFile(outside, []byte("AK=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "innocent.csv")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}

	s := newUploadServer(t, true, []string{root})
	if _, _, err := s.resolveUploadPath(link); err == nil {
		t.Fatal("expected error for symlink pointing outside the allowed directories")
	}
}

func TestResolveUploadPathRejectsNonRegularFile(t *testing.T) {
	root := t.TempDir()
	s := newUploadServer(t, true, []string{root})
	if _, _, err := s.resolveUploadPath(root); err == nil {
		t.Fatal("expected error for a directory path")
	}
}

func TestResolveUploadPathRemoteWithoutRootsIsFailClosed(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "data.csv")
	if err := os.WriteFile(file, []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	s := newUploadServer(t, true, nil)
	if _, _, err := s.resolveUploadPath(file); err == nil {
		t.Fatal("expected HTTP transport without an allowlist to reject uploads")
	}
}

func TestResolveUploadPathStdioWithoutRootsAllowsAnyFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "data.csv")
	if err := os.WriteFile(file, []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	s := newUploadServer(t, false, nil)
	if _, _, err := s.resolveUploadPath(file); err != nil {
		t.Fatalf("stdio without allowlist should stay unrestricted, got %v", err)
	}
}

func TestSetUploadRootsSkipsMissingDirs(t *testing.T) {
	root := t.TempDir()
	s := newUploadServer(t, true, []string{root, filepath.Join(root, "does-not-exist"), ""})
	if len(s.uploadRoots) != 1 {
		t.Fatalf("expected only the existing root to be kept, got %v", s.uploadRoots)
	}
}

func TestIsRemoteTransport(t *testing.T) {
	for transport, want := range map[string]bool{
		"":                false,
		"stdio":           false,
		"sse":             true,
		"streamable-http": true,
		"bogus":           false,
	} {
		if got := isRemoteTransport(transport); got != want {
			t.Errorf("isRemoteTransport(%q) = %v, want %v", transport, got, want)
		}
	}
}

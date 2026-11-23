//go:build unix

package session

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestSEC01NamedPipesAreNotStateOrImages(t *testing.T) {
	for _, name := range []string{"status.json", "images/img_0.png"} {
		t.Run(name, func(t *testing.T) {
			m := testManager(t, nil, t.TempDir())
			if err := (&State{SessionID: "s1"}).persist(m.store); err != nil {
				t.Fatal(err)
			}
			if err := m.store.writeImage("s1", "img_0.png", []byte("local")); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(m.store.root.Name(), "s1", name)
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := syscall.Mkfifo(path, 0600); err != nil {
				t.Fatal(err)
			}
			finished := make(chan error, 1)
			go func() {
				_, err := m.ReadImages("s1")
				finished <- err
			}()
			if err := managerReceive(t, finished); !errors.Is(err, ErrSessionUnavailable) {
				t.Fatalf("FIFO read accepted: %v", err)
			}
			if err := m.store.writeImage("s1", "img_0.png", []byte("overwrite")); err == nil {
				t.Fatal("FIFO write accepted")
			}
			if name == "status.json" {
				if _, err := m.GetStatus("s1"); !errors.Is(err, ErrSessionUnavailable) {
					t.Fatal("FIFO state accepted")
				}
				if err := (&State{SessionID: "s1"}).persist(m.store); err == nil {
					t.Fatal("FIFO state overwritten")
				}
			}
		})
	}
}

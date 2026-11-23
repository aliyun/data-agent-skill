package session

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

var (
	ErrInvalidSessionID   = errors.New("invalid session_id")
	ErrSessionUnavailable = errors.New("session not found or unavailable")
	ErrManagerClosed      = errors.New("session manager is closed")
	errStorage            = errors.New("session storage unavailable")
)

// ValidateSessionID implements the local, cross-platform identifier contract.
// Never normalize, decode, or include a rejected identifier in an error.
func ValidateSessionID(id string) error {
	if len(id) < 1 || len(id) > 128 {
		return ErrInvalidSessionID
	}
	for i := range len(id) {
		c := id[i]
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-') {
			return ErrInvalidSessionID
		}
	}
	upper := strings.ToUpper(id)
	switch upper {
	case "CON", "PRN", "AUX", "NUL":
		return ErrInvalidSessionID
	}
	if len(upper) == 4 && (strings.HasPrefix(upper, "COM") || strings.HasPrefix(upper, "LPT")) && upper[3] >= '1' && upper[3] <= '9' {
		return ErrInvalidSessionID
	}
	return nil
}

// Image contains a generated filename and bytes, never a filesystem path.
type Image struct {
	Name string
	Data []byte
}

// sessionStore owns the tenant root. The write lock also serializes initial
// state creation for differently-cased IDs on case-insensitive filesystems.
type sessionStore struct {
	root *os.Root
	mu   sync.Mutex
}

func openSessionStore(dir string) (*sessionStore, error) {
	return openSessionStoreMode(dir, true)
}

func openSessionStoreMode(dir string, create bool) (*sessionStore, error) {
	if dir == "" {
		return nil, errStorage
	}
	// Only bootstrap uses an administrator-provided absolute path. All storage
	// access after this point uses the pinned root, including directory creation.
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, errStorage
	}
	if create {
		if err := os.MkdirAll(filepath.Dir(abs), 0700); err != nil {
			return nil, errStorage
		}
	}
	parent, err := os.OpenRoot(filepath.Dir(abs))
	if err != nil {
		return nil, errStorage
	}
	defer parent.Close()
	root, err := openDirectory(parent, filepath.Base(abs), create)
	if err != nil {
		return nil, errStorage
	}
	return &sessionStore{root: root}, nil
}

// openDirectory refuses links, checks the opened object, and pins each level.
// Root, not these checks, enforces containment during replacement races.
func openDirectory(root *os.Root, name string, create bool) (*os.Root, error) {
	before, err := root.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) && create {
		if err = root.Mkdir(name, 0700); err != nil && !errors.Is(err, fs.ErrExist) {
			return nil, err
		}
		before, err = root.Lstat(name)
	}
	if err != nil {
		return nil, err
	}
	if !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		return nil, errStorage
	}
	child, err := root.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	after, err := child.Stat(".")
	current, currentErr := root.Lstat(name)
	if err != nil || currentErr != nil || !current.IsDir() || !os.SameFile(before, after) || !os.SameFile(before, current) {
		child.Close()
		return nil, errStorage
	}
	return child, nil
}

func readRegular(root *os.Root, name string) ([]byte, error) {
	before, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, errStorage
	}
	file, err := root.OpenFile(name, os.O_RDONLY|regularReadFlags, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	after, err := file.Stat()
	current, currentErr := root.Lstat(name)
	if err != nil || currentErr != nil || !after.Mode().IsRegular() || !current.Mode().IsRegular() || !os.SameFile(before, after) || !os.SameFile(before, current) {
		return nil, errStorage
	}
	return io.ReadAll(file)
}

func readSnapshot(root *os.Root, id string) (*StateSnapshot, error) {
	data, err := readRegular(root, "status.json")
	if err != nil {
		return nil, err
	}
	var snap StateSnapshot
	if json.Unmarshal(data, &snap) != nil || snap.SessionID != id {
		return nil, errStorage
	}
	return &snap, nil
}

func (s *sessionStore) load(id string) (*StateSnapshot, error) {
	if err := ValidateSessionID(id); err != nil {
		return nil, err
	}
	dir, err := openDirectory(s.root, id, false)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	return readSnapshot(dir, id)
}

// loadState is for read-only best-effort history/status paths. Mutating paths
// use load directly so corrupt, mismatched, or inaccessible state cannot be
// mistaken for an absent snapshot and replaced.
func loadState(store *sessionStore, id string) *StateSnapshot {
	snap, _ := store.load(id)
	return snap
}

func (s *sessionStore) persist(snap StateSnapshot) error {
	if err := ValidateSessionID(snap.SessionID); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	dir, err := openDirectory(s.root, snap.SessionID, true)
	if err != nil {
		return errStorage
	}
	defer dir.Close()
	check := func() error {
		_, err := readSnapshot(dir, snap.SessionID)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	if err := check(); err != nil {
		return errStorage
	}
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return errStorage
	}
	if err := atomicWrite(dir, "status.json", data, check); err != nil {
		return errStorage
	}
	return nil
}

func atomicWrite(root *os.Root, name string, data []byte, check func() error) error {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return err
	}
	tmp := ".tmp-" + hex.EncodeToString(random[:])
	file, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer root.Remove(tmp)
	if _, err = file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err = file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if err = check(); err != nil {
		return err
	}
	if err = root.Rename(tmp, name); err != nil {
		return err
	}
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func validImageName(name string) bool {
	stem, ext, ok := strings.Cut(name, ".")
	if !ok || !strings.HasPrefix(stem, "img_") || len(stem) <= 4 {
		return false
	}
	for _, c := range stem[4:] {
		if c < '0' || c > '9' {
			return false
		}
	}
	switch ext {
	case "png", "jpg", "jpeg", "webp", "gif":
		return true
	}
	return false
}

func (s *sessionStore) writeImage(id, name string, data []byte) error {
	if err := ValidateSessionID(id); err != nil {
		return err
	}
	if !validImageName(name) {
		return errStorage
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	dir, err := openDirectory(s.root, id, false)
	if err != nil {
		return errStorage
	}
	defer dir.Close()
	if _, err := readSnapshot(dir, id); err != nil {
		return errStorage
	}
	images, err := openDirectory(dir, "images", true)
	if err != nil {
		return errStorage
	}
	defer images.Close()
	check := func() error {
		info, err := images.Lstat(name)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil || !info.Mode().IsRegular() {
			return errStorage
		}
		return nil
	}
	if err := check(); err != nil {
		return errStorage
	}
	if err := atomicWrite(images, name, data, check); err != nil {
		return errStorage
	}
	return nil
}

func (s *sessionStore) readImages(id string) ([]Image, error) {
	if err := ValidateSessionID(id); err != nil {
		return nil, err
	}
	dir, err := openDirectory(s.root, id, false)
	if err != nil {
		return nil, ErrSessionUnavailable
	}
	defer dir.Close()
	if _, err := readSnapshot(dir, id); err != nil {
		return nil, ErrSessionUnavailable
	}
	images, err := openDirectory(dir, "images", false)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, ErrSessionUnavailable
	}
	defer images.Close()
	entries, err := readEntries(images)
	if err != nil {
		return nil, ErrSessionUnavailable
	}
	var result []Image
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".tmp-") && entry.Type().IsRegular() {
			continue
		}
		if !validImageName(entry.Name()) {
			return nil, ErrSessionUnavailable
		}
		data, err := readRegular(images, entry.Name())
		if err != nil {
			return nil, ErrSessionUnavailable
		}
		result = append(result, Image{Name: entry.Name(), Data: data})
	}
	return result, nil
}

func readEntries(root *os.Root) ([]os.DirEntry, error) {
	file, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer file.Close()
	entries, err := file.ReadDir(-1)
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	return entries, err
}

package tenant

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
)

var errTenantStorage = errors.New("tenant session storage ownership rejected")
var principalIDPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type ownerRecord struct {
	Version     int    `json:"version"`
	PrincipalID string `json:"principal_id"`
}

// openTenantRoot never consults legacy identity/ directories. Every operation
// below the administrator-controlled sessions directory uses an os.Root.
func openTenantRoot(base, id string) (*os.Root, string, error) {
	if !principalIDPattern.MatchString(id) {
		return nil, "", errTenantStorage
	}
	base, err := filepath.Abs(base)
	if err != nil {
		return nil, "", errTenantStorage
	}
	root, err := openStorageBase(base)
	if err != nil {
		return nil, "", errTenantStorage
	}
	defer root.Close()
	layout, err := openPrivateDir(root, "identity-v2")
	if err != nil {
		return nil, "", errTenantStorage
	}
	defer layout.Close()
	tenant, err := openPrivateDir(layout, id)
	if err != nil {
		return nil, "", errTenantStorage
	}
	if err = verifyOwner(tenant, id); err != nil {
		tenant.Close()
		return nil, "", errTenantStorage
	}
	return tenant, filepath.Join(base, "identity-v2", id), nil
}

// Missing configured parents are created privately. Existing storage roots
// must be process-owned and not writable by another account; no chmod occurs.
func openStorageBase(path string) (*os.Root, error) {
	parent := filepath.Dir(path)
	if parent == path {
		return nil, errTenantStorage
	}
	// Resolve the configured parent's aliases (e.g. macOS /var -> /private/var),
	// not the tenant directory or anything inside it.
	resolved, err := filepath.EvalSymlinks(parent)
	if os.IsNotExist(err) {
		p, e := openStorageBase(parent)
		if e != nil {
			return nil, e
		}
		defer p.Close()
		return openPrivateDir(p, filepath.Base(path))
	}
	if err != nil {
		return nil, err
	}
	p, err := os.OpenRoot(resolved)
	if err != nil {
		return nil, err
	}
	defer p.Close()
	actual, err := p.Stat(".")
	if err != nil || !trustedStorageParent(actual) {
		return nil, errTenantStorage
	}
	for dir := resolved; ; dir = filepath.Dir(dir) {
		info, err := os.Stat(dir)
		if err != nil || !trustedStorageParent(info) {
			return nil, errTenantStorage
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}
	return openPrivateDir(p, filepath.Base(path))
}

func openPrivateDir(parent *os.Root, name string) (*os.Root, error) {
	if err := parent.Mkdir(name, 0700); err != nil && !os.IsExist(err) {
		return nil, err
	}
	before, err := parent.Lstat(name)
	if err != nil || !before.IsDir() || !safeStorageOwner(before, false) {
		return nil, errTenantStorage
	}
	root, err := parent.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	f, err := root.Open(".")
	if err != nil {
		root.Close()
		return nil, err
	}
	after, err := f.Stat()
	f.Close()
	current, currentErr := parent.Lstat(name)
	if err != nil || currentErr != nil || !current.IsDir() || !os.SameFile(before, current) || !os.SameFile(before, after) || !safeStorageOwner(after, false) {
		root.Close()
		return nil, errTenantStorage
	}
	return root, nil
}

func verifyOwner(root *os.Root, id string) error {
	info, err := root.Lstat("owner.json")
	if err == nil {
		if !info.Mode().IsRegular() || !safeStorageOwner(info, true) {
			return errTenantStorage
		}
		f, err := root.OpenFile("owner.json", os.O_RDONLY|storageNonblock(), 0)
		if err != nil {
			return err
		}
		defer f.Close()
		actual, err := f.Stat()
		current, currentErr := root.Lstat("owner.json")
		if err != nil || currentErr != nil || !current.Mode().IsRegular() || !os.SameFile(info, current) || !os.SameFile(info, actual) || !safeStorageOwner(actual, true) {
			return errTenantStorage
		}
		data, err := io.ReadAll(io.LimitReader(f, 1025))
		if err != nil || len(data) > 1024 || !strictJWTJSON(data) {
			return errTenantStorage
		}
		var owner ownerRecord
		d := json.NewDecoder(bytes.NewReader(data))
		d.DisallowUnknownFields()
		if d.Decode(&owner) != nil || owner.Version != 2 || owner.PrincipalID != id {
			return errTenantStorage
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	names, err := dir.Readdirnames(1)
	if len(names) != 0 || err != io.EOF {
		return errTenantStorage
	}
	random := make([]byte, 16)
	if _, err = rand.Read(random); err != nil {
		return err
	}
	temp := ".owner-" + hex.EncodeToString(random)
	f, err := root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer root.Remove(temp)
	data, _ := json.Marshal(ownerRecord{Version: 2, PrincipalID: id})
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	// Only this registry initializes a principal at a time. Ordinary callers
	// cannot write the controlled root; multi-process shared roots are unsupported.
	if err = root.Rename(temp, "owner.json"); err != nil {
		return err
	}
	return dir.Sync()
}

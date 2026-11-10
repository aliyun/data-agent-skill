package config

import (
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

var errSecretFile = errors.New("secret configuration file is unsafe: use process-owned 0600/0400 regular files in trusted directories or managed environment injection; Windows file secrets require verified ACLs")

// readConfigFile validates the very handle from which data is read, not a
// separate pathname. No permissions are changed. Final symlinks are rejected;
// projected secrets must instead use environment injection or a regular file.
func readConfigFile(path string, containsSecret func([]byte) bool) ([]byte, error) {
	parent, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	parent, err = filepath.Abs(parent)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(parent)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	before, err := root.Lstat(filepath.Base(path))
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, errSecretFile
	}
	f, err := root.OpenFile(filepath.Base(path), os.O_RDONLY|nonblockFlag(), 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	current, currentErr := root.Lstat(filepath.Base(path))
	if !info.Mode().IsRegular() || !os.SameFile(before, info) || currentErr != nil || !current.Mode().IsRegular() || !os.SameFile(before, current) {
		return nil, errSecretFile
	}
	data, err := io.ReadAll(io.LimitReader(f, 4*1024*1024+1))
	if err != nil {
		return nil, errors.New("cannot read configuration file")
	}
	if len(data) > 4*1024*1024 {
		return nil, errors.New("configuration file too large")
	}
	if containsSecret(data) {
		private := privateSecretFile(info)
		allowLegacy := os.Getenv("DOTENV_UNSAFE_FILES_ALLOW") == "1"
		actualParent, parentErr := root.Stat(".")
		if (!private && !(allowLegacy && legacyReadableSecretFile(info))) || parentErr != nil || !trustedSecretParent(actualParent) {
			return nil, errSecretFile
		}
		for dir := parent; ; dir = filepath.Dir(dir) {
			info, err := os.Stat(dir)
			if err != nil || !trustedSecretParent(info) {
				return nil, errSecretFile
			}
			if filepath.Dir(dir) == dir {
				break
			}
		}
		if !private {
			log.Print("warning: DOTENV_UNSAFE_FILES_ALLOW=1 accepted a readable legacy secret file; use process-owned 0600/0400 files or managed environment injection")
		}
	}
	return data, nil
}

var secretPlaceholder = regexp.MustCompile(`^(\$\{[A-Za-z_][A-Za-z0-9_]*\}|<[A-Za-z0-9_-]+>)$`)

func realSecret(v string) bool {
	v = strings.TrimSpace(v)
	if v == "" {
		return false
	}
	// Only explicit, recognizable placeholders are exempt, never arbitrary
	// "test"/"secret" prefixes that could also be deployed credentials.
	if secretPlaceholder.MatchString(v) {
		return false
	}
	switch strings.ToLower(v) {
	case "your-auth-token", "your-jwt-secret", "your-shared-secret", "your-platform-token", "replace-me", "change-me", "changeme":
		return false
	}
	return true
}

func secretKey(key string) bool {
	key = strings.ToLower(key)
	switch key {
	case "auth_token", "shared_secret", "secret", "secrets", "api_key", "access_key_id", "access_key_secret", "security_token", "password":
		return true
	}
	return strings.HasSuffix(key, "_secret") || strings.HasSuffix(key, "_secrets") || strings.HasSuffix(key, "_token") || strings.HasSuffix(key, "_access_key_id") || strings.HasSuffix(key, "_api_key") || strings.HasSuffix(key, "_password")
}

func yamlContainsSecret(data []byte) bool {
	var doc yaml.Node
	if yaml.Unmarshal(data, &doc) != nil {
		return true
	} // malformed secret inputs fail closed
	type visit struct {
		node      *yaml.Node
		sensitive bool
	}
	seen := map[visit]bool{}
	var walk func(*yaml.Node, bool) bool
	walk = func(n *yaml.Node, sensitive bool) bool {
		key := visit{n, sensitive}
		if n == nil || seen[key] {
			return false
		}
		seen[key] = true
		if n.Kind == yaml.AliasNode {
			return walk(n.Alias, sensitive)
		}
		if n.Kind == yaml.ScalarNode {
			return sensitive && n.Tag != "!!null" && realSecret(n.Value)
		}
		if n.Kind == yaml.MappingNode {
			for i := 0; i+1 < len(n.Content); i += 2 {
				keyNode := n.Content[i]
				keySeen := map[*yaml.Node]bool{}
				for keyNode != nil && keyNode.Kind == yaml.AliasNode && !keySeen[keyNode] {
					keySeen[keyNode] = true
					keyNode = keyNode.Alias
				}
				key := ""
				if keyNode != nil {
					key = keyNode.Value
				}
				if walk(n.Content[i+1], sensitive || secretKey(key)) {
					return true
				}
			}
		} else {
			for _, child := range n.Content {
				if walk(child, sensitive) {
					return true
				}
			}
		}
		return false
	}
	return walk(&doc, false)
}

func dotenvValues(data []byte) map[string]string {
	values := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(strings.TrimPrefix(line, "export "), "=")
		if !ok {
			continue
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if len(value) >= 2 && (value[0] == '"' && value[len(value)-1] == '"' || value[0] == '\'' && value[len(value)-1] == '\'') {
			value = value[1 : len(value)-1]
		}
		if key != "" {
			values[key] = value
		}
	}
	return values
}

func dotenvContainsSecret(data []byte) bool {
	// Inspect every assignment, even an overwritten entry: its secret still
	// resides in the file and must not evade file confidentiality checks.
	for _, line := range strings.Split(string(data), "\n") {
		for key, value := range dotenvValues([]byte(line)) {
			if !secretKey(key) {
				continue
			}
			values := []string{value}
			if strings.HasSuffix(strings.ToLower(key), "_secrets") {
				values = strings.Split(value, ",")
			}
			for _, v := range values {
				if realSecret(v) {
					return true
				}
			}
		}
	}
	return false
}

//go:build !unix

package session

// Root enforces containment and Windows device-name rejection. Portable
// Lstat/descriptor identity checks additionally reject links and non-files.
const regularReadFlags = 0

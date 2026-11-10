package config

import "os"

func nonblockFlag() int { return 0 }

// Fail closed: POSIX mode bits do not establish Windows ACL ownership.
func privateSecretFile(os.FileInfo) bool        { return false }
func legacyReadableSecretFile(os.FileInfo) bool { return false }
func trustedSecretParent(os.FileInfo) bool      { return false }

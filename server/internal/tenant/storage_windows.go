package tenant

import "os"

func storageNonblock() int { return 0 }

// File-backed remote identity storage is blocked until equivalent ACL
// ownership checks are provided; POSIX mode bits are not proof on Windows.
func safeStorageOwner(os.FileInfo, bool) bool { return false }
func trustedStorageParent(os.FileInfo) bool   { return false }

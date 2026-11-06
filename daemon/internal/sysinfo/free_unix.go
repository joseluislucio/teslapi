//go:build unix

package sysinfo

import "golang.org/x/sys/unix"

// FreeBytes devuelve el espacio libre (para quien no es root) del sistema de ficheros de path.
func FreeBytes(path string) uint64 {
	var s unix.Statfs_t
	if unix.Statfs(path, &s) != nil {
		return 0
	}
	return s.Bavail * uint64(s.Bsize)
}

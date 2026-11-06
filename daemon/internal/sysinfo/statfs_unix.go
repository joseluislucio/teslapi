//go:build unix

package sysinfo

import "golang.org/x/sys/unix"

func freeGB(dir string) float64 {
	var s unix.Statfs_t
	if dir == "" || unix.Statfs(dir, &s) != nil {
		return 0
	}
	return float64(s.Bavail) * float64(s.Bsize) / (1 << 30)
}

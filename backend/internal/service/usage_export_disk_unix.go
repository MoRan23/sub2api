//go:build !windows

package service

import "golang.org/x/sys/unix"

func usageExportFreeDisk(path string) (uint64, error) {
	var s unix.Statfs_t
	e := unix.Statfs(path, &s)
	return s.Bavail * uint64(s.Bsize), e
}

//go:build linux || darwin

package management

import "golang.org/x/sys/unix"

// statfsDiskProbe resolves the filesystem owning path via stat(2) for the
// device id, then reads capacity via statfs(2).
func statfsDiskProbe(path string) (device uint64, total, avail int64, ok bool) {
	var st unix.Stat_t
	if err := unix.Stat(path, &st); err != nil {
		return 0, 0, 0, false
	}
	var fs unix.Statfs_t
	if err := unix.Statfs(path, &fs); err != nil {
		return 0, 0, 0, false
	}
	bsize := int64(fs.Bsize)
	return uint64(st.Dev), int64(fs.Blocks) * bsize, int64(fs.Bavail) * bsize, true
}

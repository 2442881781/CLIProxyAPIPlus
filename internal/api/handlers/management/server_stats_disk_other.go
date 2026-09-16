//go:build !linux && !darwin

package management

// statfsDiskProbe is unavailable on platforms without statfs (e.g. Windows).
func statfsDiskProbe(path string) (device uint64, total, avail int64, ok bool) {
	return 0, 0, 0, false
}

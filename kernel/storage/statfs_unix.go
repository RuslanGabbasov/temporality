//go:build unix

package storage

import "syscall"

// statfsBytes returns the total and available (unprivileged) bytes of the
// filesystem holding path. Linux and Darwin share the syscall and field names;
// in the kernel's standard Docker deployment this reflects the host FS.
func statfsBytes(path string) (total, free uint64, err error) {
	var st syscall.Statfs_t
	if err = syscall.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	bsize := uint64(st.Bsize)
	return st.Blocks * bsize, st.Bavail * bsize, nil
}

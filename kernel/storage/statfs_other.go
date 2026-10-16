//go:build !unix

package storage

import "errors"

// statfsBytes is unsupported off unix platforms; health degrades to no data.
func statfsBytes(path string) (total, free uint64, err error) {
	return 0, 0, errors.New("statfs is not supported on this platform")
}

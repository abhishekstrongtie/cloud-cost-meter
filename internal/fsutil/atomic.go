// Package fsutil holds small file helpers shared by the packages that keep state on disk.
package fsutil

import (
	"os"
	"path/filepath"
)

// WriteFileAtomic replaces path with data. The data goes to a temporary file in the same
// directory which is then renamed over the old file, so a crash or a failed write never
// leaves a half-written file behind. The directory is created if needed.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) (err error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()
	if _, err = tmp.Write(data); err != nil {
		return err
	}
	if err = tmp.Chmod(perm); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

package core

import (
	"errors"
	"io/fs"
	"path/filepath"
)

// DirSize returns the total size in bytes of all regular files under path,
// recursively. Symlinks are not followed and are not counted.
func DirSize(path string) (Bytes, error) {
	var total Bytes
	err := filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		total += Bytes(info.Size())
		return nil
	})
	if err != nil {
		return 0, err
	}
	return total, nil
}

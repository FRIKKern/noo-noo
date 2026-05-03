package dev

import (
	"fmt"
	"io/fs"
	"os"
)

func fileStat(p string) (fs.FileInfo, error) {
	return os.Stat(p)
}

func removeAllImpl(p string) error {
	return os.RemoveAll(p)
}

func errUnsupportedOp(op string) error {
	return fmt.Errorf("dev: unsupported op %q", op)
}

package atomicfile

import (
	"errors"
	"os"
	"path/filepath"
)

func Remove(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return syncDir(filepath.Dir(path))
}

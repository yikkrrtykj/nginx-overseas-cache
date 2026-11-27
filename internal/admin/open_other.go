//go:build !linux

package admin

import "os"

func openReadOnly(path string) (*os.File, error) { return os.Open(path) }

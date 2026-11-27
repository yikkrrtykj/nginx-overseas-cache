//go:build !unix

package atomicfile

func syncDir(string) error { return nil }

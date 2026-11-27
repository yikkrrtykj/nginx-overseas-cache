//go:build !linux

package configstore

import "context"

// Local tests use a single Store instance, serialized by its mutex.
// The privileged helper is packaged only for Linux.
func lockFile(_ context.Context, _ string) (func(), error) { return func() {}, nil }

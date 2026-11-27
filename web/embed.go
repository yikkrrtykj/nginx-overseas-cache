package web

import (
	"embed"
	"io/fs"
)

//go:embed static
var files embed.FS

func Assets() fs.FS {
	assets, err := fs.Sub(files, "static")
	if err != nil {
		panic(err)
	}
	return assets
}

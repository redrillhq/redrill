// Package web holds the built single-page UI embedded into the binary.
//
// The dist directory is produced by `make web` (Vite build) and must exist for
// the module to compile. The daemon serves these assets from internal/server.
package web

import (
	"embed"
	"io/fs"
)

//go:embed dist
var dist embed.FS

// Assets returns the built UI rooted at dist (so paths are "index.html",
// "assets/…").
func Assets() (fs.FS, error) {
	return fs.Sub(dist, "dist")
}

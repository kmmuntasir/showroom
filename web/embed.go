// Package web embeds the built dashboard SPA (demos/web/dist) so the single
// static binary serves it — docs/demos.md §Control dashboard UI (Chakra UI),
// same embed model as the panel.
//
// A fresh checkout holds only dist/.gitkeep (dist/* is gitignored), which
// keeps `go build` and `go test` working before the first `npm run build`.
// `all:dist` is deliberate: it picks up Vite's hashed assets even though
// they start with ordinary names, and dist is a build output we fully
// control.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Dist returns the embedded dist/ subtree rooted at its own filesystem, so
// handlers open "index.html" and "assets/..." directly.
func Dist() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		// Unreachable: "dist" is a literal directory embedded above.
		panic("web: sub dist: " + err.Error())
	}
	return sub
}

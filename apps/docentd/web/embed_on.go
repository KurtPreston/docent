//go:build embed

// Package web provides access to the built dashboard assets. In embed builds
// (go build -tags embed) the compiled Vite output under dist/ is baked into the
// binary so docentd serves the dashboard with no external files.
package web

import (
	"embed"
	"io/fs"
)

// The Vite build writes to dist/. `all:` includes files that would otherwise be
// skipped (e.g. dotfiles). dist/ must exist at build time (run `npm run build`).
//
//go:embed all:dist
var dist embed.FS

// FS returns the embedded dist/ tree rooted at its contents (so "index.html"
// resolves directly), or nil if the embed could not be sub-rooted.
func FS() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		return nil
	}
	return sub
}

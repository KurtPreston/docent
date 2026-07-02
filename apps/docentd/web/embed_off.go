//go:build !embed

// Package web provides access to the built dashboard assets. Without the embed
// build tag no assets are baked in; docentd falls back to serving the dashboard
// from disk via the -web flag. This keeps bare `go build`/`go vet`/`go test`
// Node-free and independent of a dist/ directory.
package web

import "io/fs"

// FS returns nil in non-embed builds, signalling disk-serve mode.
func FS() fs.FS { return nil }

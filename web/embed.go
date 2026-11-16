// Package web embeds the built SvelteKit static site (web/build, produced by
// `pnpm build` with adapter-static) directly into the Go binary.
//
// build/ is not committed (PLAN 38). The image build runs `pnpm build` from
// web/src and copies the result in, so the deployed binary always embeds the
// bundle its own commit produced; nothing has to be kept in sync by hand.
//
// The one consequence worth knowing: `go:embed all:build` matches nothing when
// the directory is absent, so a fresh clone cannot `go build ./...` or
// `go test ./...` until `make web` has run. CI does exactly that before its Go
// steps. A placeholder file would make a UI-less binary build silently, which is
// worse than an error that names the missing directory.
package web

import "embed"

// all: prefix is required: Vite's output includes an "_app" directory, and
// Go's embed skips paths starting with "_" or "." by default.
//
//go:embed all:build
var Assets embed.FS

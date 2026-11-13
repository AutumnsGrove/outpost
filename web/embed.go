// Package web embeds the built SvelteKit static site (web/build, produced by
// `pnpm build` with adapter-static) directly into the Go binary.
//
// The build output is committed on purpose, the same way bwmon and Polaris do
// it: the Le Potato never needs Node or pnpm installed, so deploying is
// `git pull && go build`. Rebuild and commit build/ whenever the frontend
// changes — `make web` does exactly that.
package web

import "embed"

// all: prefix is required: Vite's output includes an "_app" directory, and
// Go's embed skips paths starting with "_" or "." by default.
//
//go:embed all:build
var Assets embed.FS

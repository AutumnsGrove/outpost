import { createHash } from 'node:crypto';
import { existsSync, readdirSync, readFileSync, statSync } from 'node:fs';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

import adapter from '@sveltejs/adapter-static';
import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vite';

// The client's cache-invalidation token, and the reason this build is
// reproducible.
//
// SvelteKit defaults `version.name` to `Date.now()`, and the value is inlined
// into the client bundle. That made every build produce different chunk names:
// two `make web` runs on one commit minted two different sets of files, so every
// PR carried a rebuilt bundle, branches always collided in `web/build`, and "is
// the committed build in sync with the source?" could only be answered by eye —
// by reading the bundle and checking which chunk it referenced.
//
// The token is a hash of the bundle's *inputs*, which is not the same thing as
// everything in this directory. Two failure modes pull in opposite directions
// and both have to be avoided:
//
//   Too narrow, and a dependency bump or a new file in `static/` ships different
//   chunks under an identical token. An open tab then holds the old `app.js`,
//   imports a chunk the deploy deleted, gets a 404, and `updated.check()` —
//   comparing against the token that never moved — decides there is nothing to
//   reload. That is the one job the token has.
//
//   Too wide, and a change that has nothing to do with the bundle moves it:
//   `web/embed.go` is a Go file that happens to live here, and a commit touching
//   it would force a `make web` plus a bundle-only diff for no reason.
//
// So the list is explicit. A new build input has to be added here, which is the
// cost of the token meaning something.
const INPUTS = [
	'src',
	'static',
	'package.json',
	'pnpm-lock.yaml',
	'tsconfig.json',
	'vite.config.ts',
	// Optional, and absent today: corepack/pnpm do read them if they appear.
	'svelte.config.js',
	'.npmrc'
];

function buildVersion(): string {
	const root = fileURLToPath(new URL('./', import.meta.url));
	const hash = createHash('sha256');

	// Paths are built from '/', never from the platform separator: a Windows
	// build has to agree with the Linux one about what a tree's token is.
	const add = (path: string, rel: string) => {
		if (statSync(path).isDirectory()) {
			// Sorted, so the hash depends on the files rather than on the order the
			// filesystem hands them back.
			for (const name of readdirSync(path).sort()) {
				add(join(path, name), `${rel}/${name}`);
			}
			return;
		}
		hash.update(rel).update(readFileSync(path));
	};

	for (const name of INPUTS) {
		const path = join(root, name);
		if (!existsSync(path)) continue;
		add(path, name);
	}

	return hash.digest('hex').slice(0, 12);
}

export default defineConfig({
	plugins: [
		sveltekit({
			compilerOptions: {
				runes: ({ filename }) =>
					filename.split(/[/\\]/).includes('node_modules') ? undefined : true
			},
			version: {
				name: buildVersion()
			},
			// Static SPA: plain HTML/CSS/JS, no Node at runtime. The Go binary
			// embeds `build/` with go:embed and serves it, so the Le Potato needs
			// no toolchain — `git pull && go build` is the whole deploy.
			adapter: adapter({
				pages: 'build',
				assets: 'build',
				fallback: 'index.html',
				precompress: false,
				strict: true
			})
		})
	],
	server: {
		// `pnpm dev` proxies API calls to the daemon on :8896 so the page gets hot
		// reload while talking to real state.
		proxy: {
			'/api': 'http://localhost:8896'
		}
	}
});

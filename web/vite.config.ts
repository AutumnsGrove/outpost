import { createHash } from 'node:crypto';
import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join, sep } from 'node:path';
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
// A hash of everything the bundle is built from — `src/`, `static/`, the
// lockfile, the configs, anything else that is not output — is stable for a given
// tree and still changes when the bundle would, and only then: a backend commit
// no longer dirties `web/build`, and `make web-check` becomes a check that can
// fail.
//
// The inputs have to be a superset of what the output depends on, not just
// `src/`. Hashing only the sources was wrong: a dependency bump or a new file in
// `static/` produces different chunks under an identical token, and then an open
// tab holding the old `app.js` imports a chunk the deploy deleted, gets a 404,
// and `updated.check()` — comparing against the token that never moved — decides
// there is nothing to reload.
function buildVersion(): string {
	const root = fileURLToPath(new URL('./', import.meta.url));
	const skip = new Set(['build', 'node_modules', '.svelte-kit', '.DS_Store']);
	const hash = createHash('sha256');

	const walk = (dir: string) => {
		// Sorted, so the hash depends on the files rather than on the order the
		// filesystem hands them back.
		for (const name of readdirSync(dir).sort()) {
			if (skip.has(name)) continue;
			const path = join(dir, name);
			if (statSync(path).isDirectory()) {
				walk(path);
				continue;
			}
			// Slashes, never the platform separator: a Windows build has to agree with
			// the Linux one about what a tree's token is.
			hash.update(path.slice(root.length).split(sep).join('/')).update(readFileSync(path));
		}
	};
	walk(root);

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

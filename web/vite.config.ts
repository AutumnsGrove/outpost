import adapter from '@sveltejs/adapter-static';
import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vite';

export default defineConfig({
	plugins: [
		sveltekit({
			compilerOptions: {
				runes: ({ filename }) =>
					filename.split(/[/\\]/).includes('node_modules') ? undefined : true
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

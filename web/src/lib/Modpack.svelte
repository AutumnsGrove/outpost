<script lang="ts">
	import Package from '@lucide/svelte/icons/package';
	import Check from '@lucide/svelte/icons/check';
	import Download from '@lucide/svelte/icons/download';
	import { onMount } from 'svelte';
	import {
		bytes,
		getPack,
		packInstall,
		packPreview,
		packUse,
		type PackJob,
		type PackOverview
	} from './api';

	// `state` would shadow the $state rune, so the prop is named for what it is.
	let {
		token,
		serverState,
		busy = $bindable(false)
	}: { token: string; serverState: string; busy: boolean } = $props();

	let pack = $state<PackOverview | null>(null);
	let source = $state('');
	let memory = $state(4);
	let message = $state('');
	let failed = $state(false);
	// The preview is held here rather than read back off the job so it survives the
	// job being replaced by an install.
	let preview = $state<PackJob | null>(null);
	let job = $state<PackJob | null>(null);
	// The world question, answered by the checkbox the preview shows.
	let replaceWorld = $state(false);

	// A pack can only be installed or swapped while nothing is rented: a running
	// server already pulled one profile's assets and will upload its world to that
	// profile.
	const editable = $derived(serverState === 'OFFLINE' && (pack?.editable ?? true));
	const running = $derived(!!job && !['done', 'failed'].includes(job.phase));

	// Tell the page when a job is in flight: Start has to be disabled, because a
	// start mid-install rents a server that pulls a half-rewritten profile.
	$effect(() => {
		busy = running;
	});

	async function refresh() {
		try {
			pack = await getPack();
			job = pack.job ?? null;
			if (job?.kind === 'preview' && job.phase === 'done' && job.result) preview = job;
			if (job?.kind === 'install' && job.phase === 'done') {
				preview = null;
				source = '';
			}
		} catch (err) {
			message = err instanceof Error ? err.message : String(err);
			failed = true;
		}
	}

	async function resolve() {
		if (!source.trim() || busy || running) return;
		busy = true;
		message = '';
		failed = false;
		preview = null;
		replaceWorld = false;
		try {
			job = await packPreview(source.trim(), token, memory);
			await refresh();
		} catch (err) {
			message = err instanceof Error ? err.message : String(err);
			failed = true;
		} finally {
			busy = false;
		}
	}

	async function install(newWorld: boolean) {
		if (!preview || busy || running) return;
		busy = true;
		message = '';
		failed = false;
		try {
			job = await packInstall(
				{
					source: preview.source,
					profile: preview.profile,
					memoryGB: memory,
					// The world question is answered by the checkbox the preview shows,
					// and only when the profile is actually holding someone else's world.
					newWorld,
					keepWorld: !newWorld && !!mismatch,
					activate: true
				},
				token
			);
			await refresh();
		} catch (err) {
			message = err instanceof Error ? err.message : String(err);
			failed = true;
		} finally {
			busy = false;
		}
	}

	async function use(profile: string) {
		if (busy) return;
		busy = true;
		message = '';
		failed = false;
		try {
			const res = await packUse(profile, token);
			message = `${res.active} is now the active profile — the next start boots it`;
			await refresh();
		} catch (err) {
			message = err instanceof Error ? err.message : String(err);
			failed = true;
		} finally {
			busy = false;
		}
	}

	// The world in the target profile was made by a different pack: say so, and let
	// the checkbox decide. A world from the same pack at an older version is an
	// update, not a question.
	const mismatch = $derived.by(() => {
		if (!preview?.result?.worldPack) return null;
		const w = preview.result.worldPack;
		const p = preview.result.pack;
		if (w.source === p.source && w.slug === p.slug) return null;
		return w;
	});

	const phaseLabel = $derived.by(() => {
		if (!job) return '';
		if (job.phase === 'failed') return job.error || 'failed';
		if (job.phase === 'done') return job.kind === 'preview' ? 'resolved' : 'installed';
		if (job.phase === 'archive') return job.kind === 'preview' ? 'fetching the pack manifest' : 'fetching the pack manifest';
		if (job.phase === 'files') return `downloading mods — ${job.done} of ${job.total}`;
		if (job.phase === 'upload') return `uploading to R2 — ${job.done} of ${job.total}`;
		return job.phase;
	});

	const percent = $derived(job && job.total > 0 ? Math.round((job.done / job.total) * 100) : 0);

	onMount(() => {
		refresh();
		// Poll while something is in flight, and not otherwise: the profile list only
		// changes when this page (or the CLI) changes it.
		const tick = setInterval(async () => {
			if (document.visibilityState !== 'visible') return;
			if (running || !pack) {
				await refresh();
				return;
			}
		}, 2000);
		return () => clearInterval(tick);
	});
</script>

<section class="card" aria-labelledby="pack-h">
	<h2 id="pack-h"><Package aria-hidden="true" />Modpack</h2>

	{#if pack && pack.profiles.length > 0}
		<ul class="profiles">
			{#each pack.profiles as p (p.id)}
				<li class:active={p.active}>
					<span class="who">
						<span class="name mono">{p.id}</span>
						{#if p.active}<span class="badge" title="the next start boots this profile">active</span>{/if}
						<span class="meta muted">
							{p.pack ? `${p.pack.name} ${p.pack.versionNumber}` : 'hand-rolled pack'}
							· {p.mods} mod{p.mods === 1 ? '' : 's'}
							{#if p.bytes > 0}· {bytes(p.bytes)}{/if}
							· {p.hasWorld ? 'has a world' : 'no world yet'}
						</span>
					</span>
					{#if !p.active}
						<button
							class="btn quiet small"
							disabled={busy || running || !editable}
							title={editable ? 'Boot this pack and its world next time' : 'Save and stop the server first'}
							onclick={() => use(p.id)}
						>
							Use
						</button>
					{/if}
				</li>
			{/each}
		</ul>
	{:else if pack}
		<p class="muted">Nothing installed yet. Paste a Modrinth link below.</p>
	{:else}
		<p class="muted skel" aria-hidden="true">loading…</p>
	{/if}

	<form
		onsubmit={(e) => {
			e.preventDefault();
			resolve();
		}}
	>
		<label class="sr" for="pack-source">Modrinth modpack link or slug</label>
		<input
			id="pack-source"
			class="field mono"
			placeholder="https://modrinth.com/modpack/…"
			bind:value={source}
			disabled={!editable || running}
			autocomplete="off"
			autocapitalize="off"
			spellcheck="false"
		/>
		<label class="sr" for="pack-memory">RAM in GB</label>
		<input
			id="pack-memory"
			class="field mem mono"
			type="number"
			min="2"
			max="32"
			bind:value={memory}
			disabled={!editable || running}
			title="RAM of the box you will rent. Sizes start.sh and is checked against the plan."
		/>
		<button class="btn primary small" disabled={!editable || running || busy || !source.trim()}>
			<Download aria-hidden="true" />
			{busy && !job ? 'asking…' : 'Resolve'}
		</button>
	</form>

	{#if job}
		<p class="phase" class:bad={job.phase === 'failed'} role="status" aria-live="polite">
			{phaseLabel}
		</p>
		{#if running && job.total > 1}
			<div class="bar" aria-hidden="true"><i style="width: {percent}%"></i></div>
		{/if}
	{/if}

	{#if preview?.result}
		<div class="preview">
			<p class="title">
				<strong>{preview.result.pack.name} {preview.result.pack.versionNumber}</strong>
				<span class="muted mono">
					minecraft {preview.result.pack.mc} · {preview.result.pack.loader}
					{preview.result.pack.loaderVersion}
				</span>
			</p>
			<p class="muted">
				{preview.result.files} file{preview.result.files === 1 ? '' : 's'}
				· {preview.result.mods.length} mod{preview.result.mods.length === 1 ? '' : 's'}
				· {bytes(preview.result.bytes)} to download
				{#if preview.result.clientOnly?.length}
					· {preview.result.clientOnly.length} client-only skipped
				{/if}
				→ <span class="mono">{preview.result.profile}</span>
			</p>

			{#each preview.result.warnings ?? [] as w (w)}
				<p class="warn">{w}</p>
			{/each}

			{#if preview.result.mods.length}
				<details>
					<summary>{preview.result.mods.length} mods</summary>
					<ul class="mods">
						{#each preview.result.mods as m (m.path)}
							<li>
								<span class="mono nm">{m.path.replace(/^mods\//, '')}</span>
								<span class="muted sz">{bytes(m.size)}</span>
							</li>
						{/each}
					</ul>
				</details>
			{/if}

			{#if mismatch}
				<label class="replace">
					<input type="checkbox" bind:checked={replaceWorld} disabled={!editable || running} />
					<span>
						<span class="warn inline">
							<span class="mono">{preview.result.profile}</span> already has a world from
							{mismatch.name} {mismatch.versionNumber}.
						</span>
						Replace it with a fresh world. Leave it unticked to keep the one you have.
					</span>
				</label>
			{/if}

			<button
				class="btn primary small"
				disabled={!editable || running || busy}
				onclick={() => install(mismatch ? replaceWorld : false)}
			>
				{running ? 'installing…' : `Install into ${preview.result.profile}`}
			</button>
		</div>
	{:else if job?.kind === 'install' && job.phase === 'done' && job.result}
		<div class="preview done">
			<p class="title">
				<Check aria-hidden="true" />
				<strong>{job.result.pack.name} {job.result.pack.versionNumber}</strong>
				installed into <span class="mono">{job.result.profile}</span>
			</p>
			<p class="muted">
				{job.result.files} file{job.result.files === 1 ? '' : 's'} in R2. It is the active
				profile, so Start boots it.
			</p>
		</div>
	{/if}

	{#if message}
		<p class="status" class:bad={failed} role="status" aria-live="polite">{message}</p>
	{/if}

	{#if !editable}
		<p class="muted note">
			{pack?.detail ?? `Save and stop the ${serverState.toLowerCase()} server before changing packs.`}
		</p>
	{:else}
		<p class="muted note">
			Each pack keeps its own world, so switching back brings that world back. The player list
			is shared by all of them.
		</p>
	{/if}
</section>

<style>
	.profiles {
		list-style: none;
		margin: 0 0 0.9rem;
		padding: 0;
		display: grid;
		gap: 0.1rem;
	}
	.profiles li {
		display: flex;
		justify-content: space-between;
		align-items: center;
		gap: 0.6rem;
		padding: 0.4rem 0;
		border-bottom: 1px solid var(--line-soft);
	}
	.profiles li:last-child {
		border-bottom: 0;
	}
	.profiles li.active .name {
		color: var(--moss);
	}
	.who {
		display: flex;
		flex-wrap: wrap;
		align-items: baseline;
		gap: 0.4rem;
		min-width: 0;
	}
	.name {
		font-size: 0.92rem;
	}
	.meta {
		font-size: 0.8rem;
	}
	form {
		display: flex;
		gap: 0.45rem;
		align-items: center;
	}
	form .field {
		flex: 1;
		min-width: 0;
	}
	form .mem {
		flex: none;
		width: 4.2rem;
		text-align: center;
	}
	.phase {
		min-height: 1.4em;
		margin: 0.55rem 0 0;
		font-size: 0.86rem;
		color: var(--moss-dim);
	}
	.phase.bad {
		color: var(--clay);
	}
	.bar {
		height: 3px;
		border-radius: 999px;
		background: var(--line);
		overflow: hidden;
		margin-bottom: 0.5rem;
	}
	.bar i {
		display: block;
		height: 100%;
		background: var(--moss);
		transition: width 0.3s ease;
	}
	.preview {
		margin-top: 0.6rem;
		padding: 0.75rem 0.85rem;
		border: 1px solid var(--line-soft);
		border-radius: 10px;
		background: var(--panel-2);
		display: grid;
		gap: 0.45rem;
	}
	.preview.done {
		border-color: color-mix(in srgb, var(--moss) 40%, var(--line-soft));
	}
	.title {
		display: flex;
		gap: 0.4rem;
		align-items: baseline;
		flex-wrap: wrap;
		margin: 0;
	}
	.title :global(svg.lucide) {
		color: var(--moss);
		width: 1rem;
		height: 1rem;
	}
	.preview p {
		margin: 0;
		font-size: 0.86rem;
	}
	.warn {
		font-size: 0.82rem;
		color: var(--amber);
		margin: 0;
	}
	.warn.inline {
		display: inline;
	}
	details summary {
		cursor: pointer;
		font-size: 0.84rem;
		color: var(--dim);
	}
	.mods {
		list-style: none;
		margin: 0.4rem 0 0;
		padding: 0;
		max-height: 15rem;
		overflow-y: auto;
		display: grid;
		gap: 0.15rem;
	}
	.mods li {
		display: flex;
		justify-content: space-between;
		gap: 0.6rem;
		font-size: 0.78rem;
		border-bottom: 1px solid var(--line-soft);
		padding-bottom: 0.15rem;
	}
	.mods .nm {
		overflow-wrap: anywhere;
	}
	.mods .sz {
		flex: none;
	}
	.replace {
		display: flex;
		gap: 0.5rem;
		align-items: flex-start;
		font-size: 0.84rem;
	}
	.replace input {
		margin-top: 0.2rem;
	}
	.status {
		min-height: 1.4em;
		margin: 0.5rem 0 0;
		font-size: 0.86rem;
		color: var(--moss);
	}
	.status.bad {
		color: var(--clay);
	}
	.note {
		font-size: 0.78rem;
		margin: 0.6rem 0 0;
	}
</style>

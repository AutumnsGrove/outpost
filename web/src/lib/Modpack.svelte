<script lang="ts">
	import Package from '@lucide/svelte/icons/package';
	import Check from '@lucide/svelte/icons/check';
	import Download from '@lucide/svelte/icons/download';
	import { onMount } from 'svelte';
	import {
		bytes,
		clock,
		getPack,
		packDelete,
		packInstall,
		packPreview,
		packUse,
		packVanilla,
		packVersions,
		packWorldDelete,
		type PackJob,
		type PackMeta,
		type PackOverview,
		type PackVersion,
		type PackVersions
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
	// Version picking. `versions` is the list for the link in the field, `chosen`
	// is the one Resolve and Install will use: empty means "the newest runnable".
	let versions = $state<PackVersions | null>(null);
	let chosen = $state<{ id: string; number: string } | null>(null);
	let showVersions = $state(false);
	let loadingVersions = $state(false);
	// Deleting is two steps and never the default: which row is asking, then which
	// of the two things goes (the world, or the whole profile).
	let deleting = $state<string | null>(null);
	let deleteBusy = $state(false);

	// "5.4.1 available" without anyone pressing Resolve: one call per installed
	// Modrinth pack, once. Keyed by profile id, and empty for a pack that is
	// already on its newest runnable build.
	let newer = $state<Record<string, string>>({});
	let checkedNewer = $state(false);
	// Which profile the field is aimed at. Empty means "derive it from the link",
	// which is right for a pasted project; clicking an update hint on a row aims
	// it at that row, because a profile published as --profile potato is not named
	// after its pack and deriving would install into a different one.
	let targetProfile = $state('');

	// A pack can only be installed or swapped while nothing is rented: a running
	// server already pulled one profile's assets and will upload its world to that
	// profile.
	const editable = $derived(serverState === 'OFFLINE' && (pack?.editable ?? true));
	const running = $derived(!!job && !['done', 'failed'].includes(job.phase));
	const activeProfile = $derived(pack?.profiles.find((p) => p.active));
	// Vanilla has no preview, so the world question arrives as a refusal naming a
	// CLI flag. When it does, offer the answer the preview's checkbox would have.
	const worldQuestion = $derived(
		job?.kind === 'install' && job.phase === 'failed' && job.error?.includes('--new-world')
			? job.error.replace(/^publish: /, '').split(';')[0] + '.'
			: ''
	);

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
				versions = null;
				chosen = null;
				showVersions = false;
				// The profile just moved, so what is "newer" has changed too.
				checkedNewer = false;
				newer = {};
				targetProfile = '';
			}
			if (pack && !checkedNewer) checkNewer();
		} catch (err) {
			message = err instanceof Error ? err.message : String(err);
			failed = true;
		}
	}

	// profileMC is the Minecraft version of the world the picker is aimed at. It is
	// deliberately about the *world*, not the manifest: the server's guard refuses a
	// Minecraft change only when there is a saved world to lose, so a profile whose
	// assets were published but never started must be able to take a 1.21.1 build
	// from the panel. Empty means there is no question to ask.
	const profileMC = $derived.by(() => {
		const list = versions;
		if (!list) return '';
		// The row's own id: a profile published as --profile potato is not named after
		// its pack, so matching on the pack instead would read another profile's world.
		const held = pack?.profiles.find((p) => p.id === list.profile);
		if (!held?.hasWorld) return '';
		return held.pack?.mc ?? '';
	});

	// blocked is why a version cannot be picked, in the same place an unbootable
	// version is refused. Two different questions share the answer: this server
	// cannot run it at all (no JDK for that Minecraft version), or it can, but not
	// into this profile — a world does not survive 1.20.1 → 1.21.1, so a runnable
	// 1.21.1 build is a new profile, not an update. The refusal has to say which,
	// or the pack looks like it has no 1.21 build at all.
	function blocked(c: PackVersion): string {
		if (!c.bootable) return c.reason ?? 'this server cannot run this version';
		if (profileMC && !c.gameVersions.includes(profileMC)) {
			return `minecraft ${c.gameVersions.join(', ')} — ${versions?.profile} holds a ${profileMC} world, which does not open in another Minecraft version; a new profile needs \`outpost publish --profile <id>\` for now`;
		}
		return '';
	}

	// identity is the line you match against your own Prism instance. A profile is
	// only useful if you know which client can join it, and two profiles on the
	// same Minecraft version are not interchangeable.
	function identity(p: PackMeta): string {
		const loader = p.loader ? `${p.loader} ${p.loaderVersion}`.trim() : 'vanilla';
		return p.mc ? `minecraft ${p.mc} · ${loader}` : loader;
	}

	async function checkNewer() {
		const known = pack?.profiles ?? [];
		if (checkedNewer || !known.length) return;
		checkedNewer = true;
		const installed = known.filter((p) => p.pack?.source === 'modrinth' && p.pack.slug);
		await Promise.all(
			installed.map(async (p) => {
				try {
					const vs = await packVersions(p.pack!.slug, token, p.id);
					// Newer is already "the newest runnable that is not the installed
					// one", so an absent Installed means there is nothing to compare.
					// The profile check is the belt: an answer about another profile's
					// build must never be shown against this row.
					if (vs.profile === p.id && vs.installed && vs.newer) newer[p.id] = vs.newer;
				} catch {
					// Modrinth being unreachable is not this panel's problem, and saying so
					// on every load would be noise.
				}
			})
		);
	}

	// Clicking the update hint is the shortcut to updating: paste the pack back
	// into the field and load its versions, with the named one already picked, so
	// the button says Update rather than making someone hunt for the version.
	async function showUpdate(id: string, p: PackMeta) {
		if (!p.slug || busy || running) return;
		const want = newer[id];
		source = p.slug;
		targetProfile = id;
		await loadVersions();
		const next = versions?.choices.find((c) => c.number === want);
		if (next && !blocked(next)) chosen = { id: next.id, number: next.number };
	}

	async function loadVersions() {
		if (!source.trim() || busy || running) return;
		busy = true;
		message = '';
		failed = false;
		try {
			versions = await packVersions(source.trim(), token, targetProfile);
			chosen = null;
			// Default to the version this profile already holds when it holds one:
			// resolving "what I have" is what someone checking versions wants most.
			const held = versions.choices.find((c) => c.installed);
			showVersions = true;
			if (!held && versions.choices.some((c) => c.bootable)) {
				// nothing installed: keep it on "newest runnable" by leaving chosen empty
			}
		} catch (err) {
			message = err instanceof Error ? err.message : String(err);
			failed = true;
			versions = null;
		} finally {
			busy = false;
		}
	}

	function pick(c: { id: string; number: string }) {
		chosen = chosen?.id === c.id ? null : c;
	}

	async function resolve() {
		if (!source.trim() || busy || running) return;
		busy = true;
		message = '';
		failed = false;
		preview = null;
		replaceWorld = false;
		try {
			job = await packPreview(source.trim(), token, memory, chosen?.id ?? '', targetProfile);
			await refresh();
		} catch (err) {
			message = err instanceof Error ? err.message : String(err);
			failed = true;
		} finally {
			busy = false;
		}
	}

	async function install(newWorld: boolean) {
		if (!preview?.result || busy || running) return;
		busy = true;
		message = '';
		failed = false;
		const result = preview.result;
		try {
			job = await packInstall(
				{
					source: preview.source,
					// The version the preview resolved, not the picker: a newer release
					// landing between preview and install must not silently change it.
					version: result.pack.versionId,
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

	// Vanilla is an install with no project behind it: the daemon writes the
	// pack.json and publishes from it, so there is nothing to preview and no
	// version to pick.
	async function installVanilla(newWorld = false) {
		if (busy || running) return;
		busy = true;
		message = '';
		failed = false;
		preview = null;
		try {
			job = await packVanilla(token, '', newWorld);
			await refresh();
		} catch (err) {
			message = err instanceof Error ? err.message : String(err);
			failed = true;
		} finally {
			busy = false;
		}
	}

	async function remove(kind: 'world' | 'profile', profile: string) {
		if (deleteBusy) return;
		deleteBusy = true;
		message = '';
		failed = false;
		try {
			const res =
				kind === 'world'
					? await packWorldDelete(token, profile)
					: await packDelete(token, profile);
			message = res.message;
			deleting = null;
			// The profile list just changed under the panel; re-read it rather than
			// leaving a row that no longer exists on screen.
			checkedNewer = false;
			newer = {};
			await refresh();
		} catch (err) {
			message = err instanceof Error ? err.message : String(err);
			failed = true;
		} finally {
			deleteBusy = false;
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

	// "Install" into a profile that already holds this pack is an update, and
	// saying so is the difference between a button someone presses and a button
	// they are afraid of.
	function label(profile: string, slug: string): string {
		const existing = pack?.profiles.find((p) => p.id === profile);
		if (!existing?.pack) return `Install into ${profile}`;
		// A hand-rolled profile names no pack, so it is not this one whatever it is.
		// Calling that "Update" reads like the jars will be kept, and they will not.
		const samePack = !!existing.pack.slug && existing.pack.slug === slug;
		if (!samePack) return `Replace ${profile}`;
		return `Update ${profile}`;
	}

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
	<h2 id="pack-h">
		<Package aria-hidden="true" />Modpack
		{#if activeProfile}
			<span class="hsub mono">
				{activeProfile.id}{#if activeProfile.pack}
					· {identity(activeProfile.pack)}{/if}
			</span>
		{/if}
	</h2>

	{#if pack && pack.profiles.length > 0}
		<ul class="profiles">
			{#each pack.profiles as p (p.id)}
				{@const meta = p.pack}
				<li class:active={p.active}>
					<span class="who">
						<span class="name mono">{p.id}</span>
						{#if p.active}<span class="badge" title="the next start boots this profile">active</span>{/if}
						<span class="meta muted">
							{meta ? [meta.name, meta.versionNumber].filter(Boolean).join(' ') : 'hand-rolled pack'}
							{#if meta?.mc}· <span class="mono">{identity(meta)}</span>{/if}
							· {p.mods} mod{p.mods === 1 ? '' : 's'}
							{#if p.bytes > 0}· {bytes(p.bytes)}{/if}
							· {p.hasWorld ? 'has a world' : 'no world yet'}
						</span>
					</span>
					{#if deleting === p.id}
						<span class="confirm" role="group" aria-label="Delete from {p.id}">
							<span class="muted ask">
								{#if p.hasWorld}
									The world ({bytes(p.worldBytes ?? 0)}) goes to trash for 14 days.
									{#if p.active}Start would make a fresh one.{/if}
								{:else}
									No world saved.
								{/if}
								Deleting the profile takes the pack's files too.
							</span>
							{#if p.hasWorld}
								<button
									class="btn danger small"
									disabled={deleteBusy}
									title="Keep the pack installed, start a fresh world next time"
									onclick={() => remove('world', p.id)}
								>
									Delete world
								</button>
							{/if}
							<button
								class="btn danger small"
								disabled={deleteBusy || p.active}
								title={p.active
									? 'This is the profile Start boots — switch away first'
									: 'Remove the pack, its backups and its files'}
								onclick={() => remove('profile', p.id)}
							>
								Delete profile
							</button>
							<button
								class="btn quiet small"
								disabled={deleteBusy}
								onclick={() => (deleting = null)}
							>
								Cancel
							</button>
						</span>
					{:else}
						<span class="row-actions">
							{#if meta && newer[p.id]}
								<button
									class="btn quiet small upd"
									disabled={busy || running || !editable}
									title={editable
										? `Update to ${newer[p.id]} — this keeps the world`
										: 'Save and stop the server first'}
									onclick={() => showUpdate(p.id, meta)}
								>
									{newer[p.id]} available
								</button>
							{/if}
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
							<button
								class="btn quiet small"
								disabled={busy || running || !editable}
								title={editable ? 'Delete its world, or the whole profile' : 'Save and stop the server first'}
								onclick={() => (deleting = p.id)}
							>
								Delete…
							</button>
						</span>
					{/if}
				</li>
			{/each}
		</ul>
	{:else if pack}
		<p class="muted">Nothing installed yet. Paste a Modrinth link below, or install vanilla.</p>
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
			oninput={() => (targetProfile = '')}
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
		<button
			class="btn quiet small"
			disabled={!editable || running || busy || !source.trim()}
			onclick={loadVersions}
			title="See every published version, which ones this server can run, and which one you have"
		>
			Versions
		</button>
		<button class="btn primary small" disabled={!editable || running || busy || !source.trim()}>
			<Download aria-hidden="true" />
			{busy && !job ? 'asking…' : chosen ? `Resolve ${chosen.number}` : 'Resolve'}
		</button>
	</form>

	<p class="vanilla muted">
		No mods at all?
		<button
			class="btn quiet small"
			disabled={!pack || !editable || running || busy}
			title="Fabric with an empty mods/ — the plain game, with nothing to resolve"
			onclick={() => installVanilla()}
		>
			Install Vanilla {pack?.supportedMc}
		</button>
	</p>

	{#if worldQuestion}
		<p class="worldq">
			<span class="muted">{worldQuestion}</span>
			<button class="btn danger small" disabled={busy || running || !editable} onclick={() => installVanilla(true)}>
				Replace its world and install
			</button>
		</p>
	{/if}

	{#if versions && showVersions}
		<div class="versionbox">
			<p class="muted vhead">
				{versions.name} · {versions.total} version{versions.total === 1 ? '' : 's'}
				{#if versions.skipped > 0}
					· showing {versions.choices.length}, {versions.skipped} hidden
				{/if}
				→ <span class="mono">{versions.profile}</span>
				<button class="linkbtn" onclick={() => (showVersions = false)}>hide</button>
			</p>
			<ul class="versions">
				{#each versions.choices as c (c.id)}
					<li class:off={!!blocked(c)} class:picked={chosen?.id === c.id}>
						<button
							class="vrow"
							disabled={!!blocked(c) || !editable || running}
							title={blocked(c) || 'Resolve this version'}
							onclick={() => pick(c)}
						>
							<span class="vnum mono">{c.number}</span>
							<span class="vtag">{c.type}</span>
							{#if c.installed}<span class="badge">installed</span>{/if}
							<span class="vmc mono">{c.gameVersions.join(', ')}</span>
							<span class="vwhen muted">{clock(c.publishedAt)}</span>
							{#if blocked(c)}
								<span class="vwhy">{blocked(c)}</span>
							{/if}
						</button>
					</li>
				{/each}
			</ul>
			{#if chosen}
				<p class="pickedline">
					Resolve will use <strong>{chosen.number}</strong>.
					<button class="linkbtn" onclick={() => (chosen = null)}>use the newest runnable instead</button>
				</p>
			{/if}
		</div>
	{/if}

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
				{running ? 'installing…' : label(preview.result.profile, preview.result.pack.slug)}
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
	/* The active profile's identity sits beside the heading, where it is read
	   before anything is clicked. */
	.hsub {
		min-width: 0;
		font-size: 0.72rem;
		letter-spacing: 0;
		text-transform: none;
		font-weight: 400;
		color: var(--faint);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.row-actions {
		display: flex;
		gap: 0.4rem;
		align-items: center;
		flex: none;
	}
	/* Two steps, and the second one names what goes and how big it is. */
	.confirm {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		justify-content: flex-end;
		gap: 0.4rem;
		flex: 1 1 auto;
	}
	.ask {
		flex: 1 1 14rem;
		text-align: right;
		font-size: 0.78rem;
		line-height: 1.35;
	}
	/* An update is information, not a warning: moss rather than the gold the
	   active badge uses, so the two do not read as the same kind of thing. */
	.upd {
		color: var(--moss);
		border-color: color-mix(in srgb, var(--moss) 40%, transparent);
	}
	.upd:hover:not(:disabled) {
		color: var(--moss);
		border-color: var(--moss);
		background: color-mix(in srgb, var(--moss) 10%, transparent);
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
	.worldq {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 0.5rem;
		margin: 0.5rem 0 0;
		font-size: 0.82rem;
	}
	/* The second way in: a pack with no project behind it. */
	.vanilla {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 0.5rem;
		margin: 0.6rem 0 0;
		font-size: 0.82rem;
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
	.versionbox {
		margin-top: 0.6rem;
		border: 1px solid var(--line-soft);
		border-radius: 10px;
		background: var(--panel-2);
		padding: 0.6rem 0.7rem;
	}
	.vhead {
		margin: 0 0 0.4rem;
		font-size: 0.8rem;
	}
	.versions {
		list-style: none;
		margin: 0;
		padding: 0;
		max-height: 17rem;
		overflow-y: auto;
		display: grid;
		gap: 0.1rem;
	}
	.vrow {
		display: grid;
		grid-template-columns: 6.5rem 3.5rem auto 1fr auto;
		gap: 0.45rem;
		align-items: baseline;
		width: 100%;
		text-align: left;
		background: none;
		border: 0;
		border-bottom: 1px solid var(--line-soft);
		padding: 0.3rem 0.2rem;
		color: var(--text);
		font: inherit;
		font-size: 0.8rem;
		cursor: pointer;
	}
	.vrow:hover:not(:disabled) {
		background: color-mix(in srgb, var(--moss) 8%, transparent);
	}
	li.off .vrow {
		color: var(--faint);
		cursor: not-allowed;
	}
	li.picked .vrow {
		background: color-mix(in srgb, var(--moss) 14%, transparent);
		color: var(--moss);
	}
	.vnum {
		font-size: 0.8rem;
	}
	.vtag {
		font-size: 0.7rem;
		text-transform: uppercase;
		letter-spacing: 0.04em;
		color: var(--faint);
	}
	.vwhen {
		font-size: 0.72rem;
		white-space: nowrap;
	}
	.vwhy {
		grid-column: 4 / -1;
		font-size: 0.74rem;
		color: var(--amber);
		text-align: right;
	}
	.pickedline {
		margin: 0.45rem 0 0;
		font-size: 0.8rem;
	}
	.linkbtn {
		background: none;
		border: 0;
		color: var(--moss);
		font: inherit;
		font-size: inherit;
		padding: 0 0.2rem;
		cursor: pointer;
		text-decoration: underline;
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

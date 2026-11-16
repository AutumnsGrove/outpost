<script lang="ts">
	import { onMount } from 'svelte';
	import Check from '@lucide/svelte/icons/check';
	import Copy from '@lucide/svelte/icons/copy';
	import RefreshCw from '@lucide/svelte/icons/refresh-cw';
	import { act, getOverview, type ActionKind, type Overview, type ServerState } from '$lib/api';
	import Hero from '$lib/Hero.svelte';
	import Players from '$lib/Players.svelte';
	import Console from '$lib/Console.svelte';
	import Modpack from '$lib/Modpack.svelte';
	import Side from '$lib/Side.svelte';

	let o = $state<Overview | null>(null);
	let error = $state('');
	let notice = $state('');
	let pending = $state<ActionKind | null>(null);
	let confirmStop = $state(false);
	let confirmTimer: ReturnType<typeof setTimeout>;
	let copied = $state(false);
	// Kept here so the Start button knows to wait: see Modpack.svelte.
	let packBusy = $state(false);
	// The plan Start rents. It opens on the one the daemon remembers (or the
	// smallest that holds the pack), and whatever is chosen here is what the
	// daemon records for next time.
	let plan = $state('');

	// The server's numbers are a snapshot from `receivedAt`; the page advances them
	// once a second in between so countdowns do not jump in five-second steps.
	let receivedAt = $state(Date.now());
	let now = $state(Date.now());
	const elapsed = $derived(Math.max(0, (now - receivedAt) / 1000));

	const label: Record<ServerState, string> = {
		OFFLINE: 'Offline',
		PROVISIONING: 'Building',
		RUNNING: 'Running',
		IDLE: 'Idle',
		TERMINATING: 'Shutting down',
		ERROR: 'Needs a human'
	};
	const color: Record<ServerState, string> = {
		OFFLINE: 'var(--slate)',
		PROVISIONING: 'var(--amber)',
		RUNNING: 'var(--moss)',
		IDLE: 'var(--gold)',
		TERMINATING: 'var(--amber)',
		ERROR: 'var(--clay)'
	};

	const phase = $derived(o?.status.state);
	const live = $derived(phase === 'RUNNING' || phase === 'IDLE');
	const dns = $derived(o?.status.dnsName ?? '');

	async function refresh() {
		try {
			const next = await getOverview();
			o = next;
			receivedAt = Date.now();
			now = receivedAt;
			error = '';
			// Follow the daemon's choice unless the picker has been touched: an
			// emptied field would otherwise send "" and let the daemon decide anyway.
			if (!plan) plan = next.selectedType;
		} catch (err) {
			error = err instanceof Error ? err.message : String(err);
		}
	}

	async function run(action: ActionKind) {
		if (!o || pending) return;
		if (packBusy) {
			error = 'a pack is being installed — wait for it to finish before starting a server';
			return;
		}
		pending = action;
		notice = '';
		error = '';
		try {
			const res = await act(action, o.status.formToken, action === 'start' ? plan || o.selectedType : '');
			notice = res.message || 'done';
			confirmStop = false;
			await refresh();
		} catch (err) {
			error = err instanceof Error ? err.message : String(err);
		} finally {
			pending = null;
		}
	}

	function askStop() {
		if (confirmStop) return run('stop');
		confirmStop = true;
		clearTimeout(confirmTimer);
		confirmTimer = setTimeout(() => (confirmStop = false), 5000);
	}

	async function copyAddress() {
		try {
			await navigator.clipboard.writeText(dns);
			copied = true;
			setTimeout(() => (copied = false), 1500);
		} catch {
			error = 'The browser blocked the clipboard. The address is ' + dns;
		}
	}

	onMount(() => {
		refresh();
		let busy = false;
		const poll = setInterval(async () => {
			// One request at a time, and none while the tab is hidden.
			if (busy || document.visibilityState !== 'visible') return;
			busy = true;
			await refresh();
			busy = false;
		}, 5000);
		const clock = setInterval(() => (now = Date.now()), 1000);
		const onVisible = () => document.visibilityState === 'visible' && refresh();
		document.addEventListener('visibilitychange', onVisible);
		return () => {
			clearInterval(poll);
			clearInterval(clock);
			clearTimeout(confirmTimer);
			document.removeEventListener('visibilitychange', onVisible);
		};
	});
</script>

<svelte:head>
	<title>Outpost{phase ? ' — ' + label[phase] : ''}</title>
</svelte:head>

<main>
	<header>
		<div class="wordmark">
			<svg viewBox="0 0 32 32" aria-hidden="true" width="26" height="26">
				<path
					d="M16 4.5 26.5 10.5v11L16 27.5 5.5 21.5v-11z"
					fill="none"
					stroke="var(--moss)"
					stroke-width="1.8"
					stroke-linejoin="round"
				/>
				<circle cx="16" cy="16" r="3" fill="var(--moss)" />
			</svg>
			<h1>Outpost</h1>
		</div>
		<div class="right">
			{#if o && dns}
				<button class="addr mono" onclick={copyAddress} aria-label="Copy the server address {dns}">
					{#if copied}<Check aria-hidden="true" />{:else}<Copy aria-hidden="true" />{/if}
					{copied ? 'copied' : dns}
				</button>
			{/if}
			{#if phase}
				<span class="pill" style="--state: {color[phase]}" role="status">
					<i></i>{label[phase]}
				</span>
			{:else}
				<span class="pill skel" style="width: 6.5rem; height: 2rem" aria-hidden="true"></span>
			{/if}
		</div>
	</header>

	<div class="banners" aria-live="polite">
		{#if error}
			<p class="banner bad" role="alert">{error}</p>
		{:else if notice}
			<p class="banner good">{notice}</p>
		{/if}
	</div>

	{#if o}
		<div class="layout">
			<div class="main">
				<Hero
					{o}
					{elapsed}
					{pending}
					{confirmStop}
					installing={packBusy}
					bind:plan
					onstart={() => run('start')}
					onstop={askStop}
				/>
				<Console {live} token={o.status.formToken} />
				<Players players={o.players} token={o.status.formToken} onchanged={refresh} />
				<Modpack token={o.status.formToken} serverState={o.status.state} bind:busy={packBusy} />
			</div>
			<aside class="side">
				<Side {o} {now} />
			</aside>
		</div>

		<footer>
			<p class="foot">
				<span class="mono">{o.build.commit}{o.build.modified ? '+' : ''}</span>
				<span aria-hidden="true">·</span>
				<span title="Consecutive failed RCON probes before a live server is taken down">
					control loop {o.health.rconFailures === 0
						? 'healthy'
						: `${o.health.rconFailures}/${o.health.rconFailureLimit} probes failed`}
				</span>
				{#if o.canUpdate}
					<span aria-hidden="true">·</span>
					<button
						class="linkbtn"
						onclick={() => run('update')}
						disabled={pending !== null || !['OFFLINE', 'ERROR'].includes(o.status.state)}
						title="Ask the host watcher for the newest image — it pulls, recreates the container, and rolls back if it does not come up. Only while offline."
					>
						<RefreshCw aria-hidden="true" />{pending === 'update' ? 'asking…' : 'update'}
					</button>
					{#if o.update?.available}
						{#if o.update.available.updateAvailable}
							<span class="muted" title="The newest image the watcher has seen on the registry">
								{o.update.available.latest} available
							</span>
						{:else}
							<span class="muted" title="Checked {o.update.available.checkedAt}">up to date</span>
						{/if}
					{/if}
					{#if o.update?.result}
						<span
							class="muted"
							title={o.update.result.detail}
							class:bad={o.update.result.status === 'failed'}
						>
							last update {o.update.result.status}
						</span>
					{/if}
				{/if}
			</p>
			<p class="links">
				<a href="https://github.com/AutumnsGrove/outpost">repo</a>
				<span aria-hidden="true">·</span>
				<a href="https://github.com/AutumnsGrove/homelab/blob/main/docs/outpost.md">docs</a>
				<span aria-hidden="true">·</span>
				<a href="https://lepotato.taild95077.ts.net:8891">meridian</a>
			</p>
		</footer>
	{:else}
		<!-- Same footprint as the real page, so the first poll moves nothing. -->
		<div class="layout" aria-busy="true" aria-label="Loading">
			<div class="main">
				<div class="skel hero-skel"></div>
				<div class="skel card-skel"></div>
				<div class="skel card-skel short"></div>
			</div>
			<aside class="side">
				<div class="skel card-skel"></div>
				<div class="skel card-skel"></div>
				<div class="skel card-skel"></div>
			</aside>
		</div>
	{/if}
</main>

<style>
	main {
		max-width: 72rem;
		margin: 0 auto;
		padding: 1.5rem 1rem 3rem;
	}
	header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 1rem;
		margin-bottom: 1.2rem;
		min-height: 44px;
	}
	.wordmark,
	.right {
		display: flex;
		align-items: center;
		gap: 0.6rem;
	}
	h1 {
		font-size: 1.3rem;
		font-weight: 620;
		letter-spacing: 0.01em;
		margin: 0;
	}
	.addr {
		display: inline-flex;
		align-items: center;
		gap: 0.4rem;
		min-height: 44px;
		background: transparent;
		border: 1px dashed var(--line);
		border-radius: 10px;
		padding: 0 0.8rem;
		color: var(--dim);
		font-size: 0.85rem;
	}
	.addr:hover {
		border-color: var(--moss-dim);
		color: var(--text);
	}
	.pill {
		display: inline-flex;
		align-items: center;
		gap: 0.45rem;
		font-size: 0.78rem;
		font-weight: 600;
		letter-spacing: 0.05em;
		text-transform: uppercase;
		color: var(--state);
		background: color-mix(in srgb, var(--state) 12%, transparent);
		border: 1px solid color-mix(in srgb, var(--state) 35%, transparent);
		border-radius: 999px;
		padding: 0.28rem 0.7rem;
	}
	.pill i {
		width: 7px;
		height: 7px;
		border-radius: 50%;
		background: var(--state);
	}
	.banners {
		min-height: 0;
	}
	.banner {
		margin: 0 0 1rem;
		padding: 0.65rem 0.85rem;
		border-radius: 10px;
		font-size: 0.9rem;
		overflow-wrap: anywhere;
	}
	.banner.good {
		background: color-mix(in srgb, var(--moss) 10%, transparent);
		border-left: 3px solid var(--moss);
	}
	.banner.bad {
		background: color-mix(in srgb, var(--clay) 10%, transparent);
		border-left: 3px solid var(--clay);
	}
	.layout {
		display: grid;
		gap: 1rem;
		grid-template-columns: minmax(0, 1fr);
		align-items: start;
	}
	.main,
	.side {
		display: grid;
		grid-template-columns: minmax(0, 1fr);
		gap: 1rem;
		min-width: 0;
	}
	@media (min-width: 62rem) {
		.layout {
			grid-template-columns: minmax(0, 1.55fr) minmax(0, 1fr);
		}
	}
	.hero-skel {
		height: 15rem;
		border-radius: var(--radius);
	}
	.card-skel {
		height: 11rem;
		border-radius: var(--radius);
	}
	.card-skel.short {
		height: 9rem;
	}
	footer {
		margin-top: 1.6rem;
		color: var(--faint);
		font-size: 0.82rem;
	}
	footer p {
		margin: 0 0 0.4rem;
		display: flex;
		flex-wrap: wrap;
		gap: 0.4rem 0.6rem;
		align-items: center;
	}
	.linkbtn {
		display: inline-flex;
		align-items: center;
		gap: 0.35rem;
		min-height: 44px;
		padding: 0 0.3rem;
		background: none;
		border: 0;
		color: var(--moss);
		text-decoration: underline;
		text-underline-offset: 2px;
	}
	.linkbtn:disabled {
		color: var(--faint);
		cursor: not-allowed;
		text-decoration: none;
	}
	.links a {
		display: inline-flex;
		align-items: center;
		min-height: 44px;
	}
	@media (max-width: 36rem) {
		.addr {
			display: none;
		}
	}
</style>

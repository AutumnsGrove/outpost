<script lang="ts">
	import { onMount } from 'svelte';
	import {
		act,
		clock,
		duration,
		euros,
		getEvents,
		getStatus,
		type ActionKind,
		type Event,
		type ServerState,
		type Status
	} from '$lib/api';

	let status = $state<Status | null>(null);
	let events = $state<Event[]>([]);
	let error = $state('');
	let notice = $state('');
	let pending = $state<ActionKind | null>(null);
	let confirmStop = $state(false);
	let copied = $state(false);

	const stateLabel: Record<ServerState, string> = {
		OFFLINE: 'Offline',
		PROVISIONING: 'Building',
		RUNNING: 'Running',
		IDLE: 'Idle',
		TERMINATING: 'Shutting down',
		ERROR: 'Needs a human'
	};

	const stateColor: Record<ServerState, string> = {
		OFFLINE: 'var(--slate)',
		PROVISIONING: 'var(--amber)',
		RUNNING: 'var(--moss)',
		IDLE: 'var(--gold)',
		TERMINATING: 'var(--amber)',
		ERROR: 'var(--clay)'
	};

	let headline = $derived.by(() => {
		if (!status) return 'Loading…';
		switch (status.state) {
			case 'OFFLINE':
				return 'The server is offline';
			case 'PROVISIONING':
				return 'Renting a server…';
			case 'RUNNING':
				return status.players > 0 ? `${status.players} playing` : 'Running, nobody on';
			case 'IDLE':
				return 'Idle — nobody playing';
			case 'TERMINATING':
				return 'Saving the world';
			case 'ERROR':
				return 'Something needs attention';
		}
	});

	let subline = $derived.by(() => {
		if (!status) return '';
		const name = status.dnsName || 'mc.grove.place';
		switch (status.state) {
			case 'OFFLINE':
				return `Starting takes about four minutes, then it is at ${name}. Nothing is billing.`;
			case 'PROVISIONING':
				return 'cloud-init is joining the tailnet and pulling the modpack from R2. The idle clock starts when RCON answers.';
			case 'RUNNING':
				return status.players > 0
					? `Shuts down ${duration(status.idleGraceSeconds + status.idleLimitSeconds)} after the last player leaves.`
					: `The idle clock is running: ${duration(status.ttlSeconds)} until it saves and deletes itself.`;
			case 'IDLE':
				return `Nobody has been on for ${duration(status.idleSeconds)}. ${duration(status.ttlSeconds)} left before it saves and shuts down.`;
			case 'TERMINATING':
				return 'save-all, stop, tar, upload to R2, then the VPS is deleted.';
			case 'ERROR':
				return status.lastError || 'A world save failed. The VPS is still running on purpose.';
		}
	});

	let canStart = $derived(status?.state === 'OFFLINE' || status?.state === 'ERROR');
	let canStop = $derived(
		status?.state === 'PROVISIONING' || status?.state === 'RUNNING' || status?.state === 'IDLE'
	);

	async function refresh() {
		try {
			const [s, e] = await Promise.all([getStatus(), getEvents()]);
			status = s;
			events = (e.events ?? []).slice(0, 6);
			error = '';
		} catch (err) {
			error = err instanceof Error ? err.message : String(err);
		}
	}

	async function run(action: ActionKind) {
		if (!status || pending) return;
		pending = action;
		notice = '';
		error = '';
		try {
			const res = await act(action, status.formToken);
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
		if (confirmStop) {
			run('stop');
			return;
		}
		confirmStop = true;
		setTimeout(() => (confirmStop = false), 5000);
	}

	async function copyAddress() {
		if (!status) return;
		try {
			await navigator.clipboard.writeText(status.dnsName || 'mc.grove.place');
			copied = true;
			setTimeout(() => (copied = false), 1500);
		} catch {
			error = 'clipboard blocked by the browser';
		}
	}

	onMount(() => {
		refresh();
		const timer = setInterval(() => {
			if (document.visibilityState === 'visible') refresh();
		}, 5000);
		const onVisible = () => document.visibilityState === 'visible' && refresh();
		document.addEventListener('visibilitychange', onVisible);
		return () => {
			clearInterval(timer);
			document.removeEventListener('visibilitychange', onVisible);
		};
	});
</script>

<svelte:head>
	<title>Outpost{status ? ' — ' + stateLabel[status.state] : ''}</title>
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
		{#if status}
			<span class="pill" style="--state: {stateColor[status.state]}">
				<i></i>{stateLabel[status.state]}
			</span>
		{/if}
	</header>

	{#if error}
		<p class="banner bad" role="alert">{error}</p>
	{:else if notice}
		<p class="banner good">{notice}</p>
	{/if}

	<section class="hero" class:offline={status?.state === 'OFFLINE'}>
		<div>
			<p class="headline" aria-live="polite">{headline}</p>
			<p class="sub">{subline}</p>
		</div>

		<div class="actions">
			{#if canStart}
				<button class="primary" onclick={() => run('start')} disabled={pending !== null}>
					{pending === 'start' ? 'Renting…' : 'Start server'}
				</button>
			{/if}
			{#if canStop}
				<button class="danger" onclick={askStop} disabled={pending !== null}>
					{pending === 'stop'
						? 'Saving…'
						: confirmStop
							? 'Really save and stop?'
							: 'Save and stop'}
				</button>
			{/if}
			<button class="quiet" onclick={() => run('update')} disabled={pending !== null || (status?.state !== 'OFFLINE' && status?.state !== 'ERROR')} title="git pull, rebuild, restart — only while offline">
				{pending === 'update' ? 'Updating…' : 'Update code'}
			</button>
		</div>
	</section>

	<div class="grid">
		<section class="card address">
			<h2>Address</h2>
			<button class="copy" onclick={copyAddress} title="copy">
				<span class="mono">{status?.dnsName || 'mc.grove.place'}</span>
				<span class="hint">{copied ? 'copied' : 'copy'}</span>
			</button>
			{#if status?.serverIp}
				<dl>
					<dt>server</dt>
					<dd class="mono">{status.serverIp}</dd>
				</dl>
			{/if}
		</section>

		<section class="card">
			<h2>This session</h2>
			<p class="big">{euros(status?.sessionCost ?? 0)}</p>
			<dl>
				<dt>uptime</dt>
				<dd class="mono">{duration(status?.uptimeSeconds ?? 0) || '—'}</dd>
				{#if status?.region}
					<dt>machine</dt>
					<dd class="mono">
						{status.region} · {status.serverType} · {euros(status.hourlyRate, 4)}/h
					</dd>
				{/if}
			</dl>
		</section>

		<section class="card">
			<h2>Lifetime</h2>
			<p class="big">{euros(status?.totalCost ?? 0)}</p>
			<dl>
				<dt>billed time</dt>
				<dd class="mono">{(status?.totalHours ?? 0).toFixed(2)} h</dd>
				<dt>players</dt>
				<dd class="mono">{status?.players ?? 0}</dd>
			</dl>
		</section>
	</div>

	{#if events.length}
		<section class="card events">
			<h2>Recent</h2>
			<ul>
				{#each events as event (event.at + event.kind + event.message)}
					<li>
						<span class="when mono">{clock(event.at)}</span>
						<span class="kind" data-kind={event.kind}>{event.kind}</span>
						<span class="what">{event.message}</span>
					</li>
				{/each}
			</ul>
		</section>
	{/if}

	<footer>
		<p>
			Saves the world to R2 and deletes the VPS when everyone logs off. From a shell:
			<code>outpost start</code>, <code>outpost stop</code>, <code>outpost status</code>.
		</p>
		<p class="links">
			<a href="https://github.com/AutumnsGrove/outpost">repo</a>
			<span>·</span>
			<a href="https://github.com/AutumnsGrove/homelab/blob/main/docs/outpost.md">docs</a>
			<span>·</span>
			<a href="https://lepotato.taild95077.ts.net:8891">meridian</a>
		</p>
	</footer>
</main>

<style>
	main {
		max-width: 46rem;
		margin: 0 auto;
		padding: 1.75rem 1rem 3rem;
	}

	header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 1rem;
		margin-bottom: 1.5rem;
	}

	.wordmark {
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
		box-shadow: 0 0 0 0 color-mix(in srgb, var(--state) 60%, transparent);
	}

	.hero {
		display: grid;
		gap: 1.1rem;
		background: linear-gradient(180deg, var(--panel-2), var(--panel));
		border: 1px solid var(--line);
		border-radius: var(--radius);
		padding: 1.3rem 1.25rem;
		margin-bottom: 1rem;
	}

	.headline {
		font-size: 1.55rem;
		font-weight: 600;
		line-height: 1.2;
		margin: 0 0 0.35rem;
	}

	.sub {
		margin: 0;
		color: var(--dim);
		font-size: 0.92rem;
		max-width: 34rem;
	}

	.actions {
		display: flex;
		flex-wrap: wrap;
		gap: 0.6rem;
	}

	button.primary,
	button.danger,
	button.quiet {
		min-height: 44px;
		border-radius: 10px;
		border: 1px solid transparent;
		padding: 0.7rem 1.15rem;
		font-weight: 600;
		transition:
			transform 80ms ease,
			background 120ms ease,
			border-color 120ms ease;
	}

	button.primary {
		background: var(--moss);
		color: #16180f;
	}

	button.danger {
		background: transparent;
		color: var(--clay);
		border-color: color-mix(in srgb, var(--clay) 45%, transparent);
	}

	button.danger:hover {
		background: color-mix(in srgb, var(--clay) 14%, transparent);
	}

	button.quiet {
		background: transparent;
		color: var(--dim);
		border-color: var(--line);
		font-weight: 500;
	}

	button.quiet:hover:not(:disabled) {
		color: var(--text);
		border-color: var(--line-soft);
	}

	button:active:not(:disabled) {
		transform: translateY(1px);
	}

	button:disabled {
		opacity: 0.45;
		cursor: not-allowed;
	}

	.banner {
		margin: 0 0 1rem;
		padding: 0.6rem 0.8rem;
		border-radius: 10px;
		font-size: 0.9rem;
	}

	.banner.good {
		background: color-mix(in srgb, var(--moss) 10%, transparent);
		border-left: 3px solid var(--moss);
	}

	.banner.bad {
		background: color-mix(in srgb, var(--clay) 10%, transparent);
		border-left: 3px solid var(--clay);
	}

	.grid {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(13rem, 1fr));
		gap: 0.8rem;
		margin-bottom: 0.8rem;
	}

	.card {
		background: var(--panel);
		border: 1px solid var(--line-soft);
		border-radius: var(--radius);
		padding: 1rem 1.05rem;
		margin-bottom: 0.8rem;
	}

	.grid .card {
		margin-bottom: 0;
	}

	h2 {
		font-size: 0.72rem;
		letter-spacing: 0.09em;
		text-transform: uppercase;
		color: var(--faint);
		margin: 0 0 0.6rem;
		font-weight: 650;
	}

	.big {
		font-family: var(--mono);
		font-size: 1.45rem;
		font-variant-numeric: tabular-nums;
		margin: 0 0 0.5rem;
	}

	dl {
		display: grid;
		grid-template-columns: auto 1fr;
		gap: 0.2rem 0.8rem;
		margin: 0;
		font-size: 0.85rem;
	}

	dt {
		color: var(--faint);
	}

	dd {
		margin: 0;
		color: var(--dim);
		overflow-wrap: anywhere;
	}

	.copy {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.8rem;
		width: 100%;
		min-height: 44px;
		background: var(--bg-glow);
		border: 1px dashed var(--line);
		border-radius: 10px;
		padding: 0.6rem 0.75rem;
		margin-bottom: 0.7rem;
		color: var(--text);
		text-align: left;
	}

	.copy:hover {
		border-color: var(--moss-dim);
	}

	.copy .hint {
		font-size: 0.72rem;
		color: var(--faint);
		text-transform: uppercase;
		letter-spacing: 0.08em;
	}

	.events ul {
		list-style: none;
		margin: 0;
		padding: 0;
		display: grid;
		gap: 0.35rem;
	}

	.events li {
		display: grid;
		grid-template-columns: 4.5rem 6.5rem 1fr;
		gap: 0.6rem;
		font-size: 0.84rem;
		color: var(--dim);
		padding: 0.15rem 0;
	}

	.when {
		color: var(--faint);
	}

	.kind {
		font-size: 0.72rem;
		letter-spacing: 0.05em;
		text-transform: uppercase;
		color: var(--moss-dim);
	}

	.kind[data-kind='error'],
	.kind[data-kind='save-failed'],
	.kind[data-kind='rcon-dead'] {
		color: var(--clay);
	}

	.kind[data-kind='idle'],
	.kind[data-kind='idle-limit'],
	.kind[data-kind='no-capacity'] {
		color: var(--amber);
	}

	.what {
		overflow-wrap: anywhere;
	}

	footer {
		margin-top: 1.4rem;
		color: var(--faint);
		font-size: 0.82rem;
	}

	footer p {
		margin: 0 0 0.4rem;
	}

	footer code {
		color: var(--dim);
	}

	.links {
		display: flex;
		gap: 0.5rem;
		align-items: center;
	}

	@media (max-width: 30rem) {
		.headline {
			font-size: 1.3rem;
		}

		.actions button {
			flex: 1 1 100%;
		}

		.events li {
			grid-template-columns: 3.6rem 1fr;
			grid-template-areas:
				'when kind'
				'what what';
		}

		.events .when {
			grid-area: when;
		}

		.events .kind {
			grid-area: kind;
		}

		.events .what {
			grid-area: what;
		}
	}
</style>

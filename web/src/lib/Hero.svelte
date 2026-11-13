<script lang="ts">
	import { bytes, ago, duration, euros, mmss, type Overview } from './api';

	let {
		o,
		elapsed,
		pending,
		confirmStop,
		onstart,
		onstop
	}: {
		o: Overview;
		elapsed: number;
		pending: string | null;
		confirmStop: boolean;
		onstart: () => void;
		onstop: () => void;
	} = $props();

	const s = $derived(o.status);
	// Values that move between polls are advanced locally, then snap back to the
	// server's number on the next poll.
	const ttl = $derived(Math.max(0, s.ttlSeconds - elapsed));
	const uptime = $derived(s.uptimeSeconds + elapsed);
	const sessionCost = $derived(s.sessionCost + (s.hourlyRate * elapsed) / 3600);
	const provElapsed = $derived(uptime);
	const lastBackup = $derived(o.backups[0]);

	// A server with an ID still exists and still bills, whatever state we are in.
	const hasServer = $derived(!!s.serverId);
	const canStart = $derived(s.state === 'OFFLINE' || (s.state === 'ERROR' && !hasServer));
	const canStop = $derived(
		['PROVISIONING', 'RUNNING', 'IDLE'].includes(s.state) || (s.state === 'ERROR' && hasServer)
	);

	// Shutdown has no progress channel, so the steps are read off the event log:
	// "saved" arriving after the stop reason means the upload is done.
	const stopReasons = ['stop', 'idle-limit', 'rcon-dead', 'provision-timeout'];
	const saved = $derived.by(() => {
		const evs = o.events; // newest first
		for (const e of evs) {
			if (e.kind === 'saved' || e.kind === 'no-world') return true;
			if (stopReasons.includes(e.kind)) return false;
		}
		return false;
	});

	const typical = 240;
	const timeout = $derived(o.health.provisionTimeoutSeconds || 900);
	const idleTotal = $derived(s.idleGraceSeconds + s.idleLimitSeconds);
</script>

<section class="hero {s.state.toLowerCase()}" aria-labelledby="hero-title">
	{#if s.state === 'OFFLINE'}
		<div class="copy">
			<p class="eyebrow">Offline · nothing is billing</p>
			<h2 id="hero-title">Ready when you are</h2>
			<p class="sub">
				Starting rents a server and restores the world. It takes about four minutes, then it is at
				<span class="mono">{s.dnsName}</span>.
			</p>
			{#if lastBackup}
				<p class="safe">
					<span class="dot ok"></span>World safe in R2 — saved {ago(lastBackup.at)}, {bytes(
						lastBackup.sizeBytes
					)}
				</p>
			{/if}
		</div>
	{:else if s.state === 'PROVISIONING'}
		<div class="copy">
			<p class="eyebrow">Building</p>
			<h2 id="hero-title">
				<span class="clock num">{mmss(provElapsed)}</span> since renting
			</h2>
			<div
				class="track"
				role="progressbar"
				aria-label="Time spent provisioning, against the {mmss(timeout)} limit"
				aria-valuemin="0"
				aria-valuemax={timeout}
				aria-valuenow={Math.min(provElapsed, timeout)}
			>
				<i style="width: {Math.min(100, (provElapsed / timeout) * 100)}%"></i>
				<b style="left: {(typical / timeout) * 100}%" title="a typical build"></b>
			</div>
			<p class="scale">
				<span>typical build ≈ {mmss(typical)}</span><span>gives up at {mmss(timeout)}</span>
			</p>
			<p class="sub">
				cloud-init is joining the tailnet and pulling the pack from R2. This is time elapsed, not
				a percentage: the server has no way to report which step it is on.
			</p>
		</div>
	{:else if s.state === 'RUNNING'}
		<div class="copy">
			<p class="eyebrow">Running · {s.region} · {s.serverType}</p>
			<h2 id="hero-title" class="big-count">
				{#if s.players > 0}
					<span class="n num">{s.players}</span> playing
				{:else}
					Nobody on yet
				{/if}
			</h2>
			{#if o.players.online.length}
				<ul class="names" aria-label="Online players">
					{#each o.players.online as name (name)}
						<li class="chip"><span class="dot ok"></span>{name}</li>
					{/each}
				</ul>
			{/if}
			<p class="sub">
				{#if s.players > 0}
					Shuts down {duration(idleTotal)} after the last player leaves.
				{:else}
					The idle clock is running: <span class="num">{duration(ttl)}</span> until it saves and
					deletes itself.
				{/if}
			</p>
		</div>
	{:else if s.state === 'IDLE'}
		<div class="copy">
			<p class="eyebrow">Idle · nobody playing</p>
			<h2 id="hero-title">
				Shutting down in <span class="clock num">{mmss(ttl)}</span>
			</h2>
			<div
				class="track drain"
				role="progressbar"
				aria-label="Time left before the server saves and shuts down"
				aria-valuemin="0"
				aria-valuemax={idleTotal}
				aria-valuenow={Math.round(ttl)}
			>
				<i style="width: {Math.min(100, (ttl / idleTotal) * 100)}%"></i>
			</div>
			<p class="sub">
				Empty for {duration(s.idleSeconds + elapsed)}. Someone joining resets the clock.
			</p>
		</div>
	{:else if s.state === 'TERMINATING'}
		<div class="copy">
			<p class="eyebrow">Shutting down</p>
			<h2 id="hero-title">Saving the world</h2>
			<ol class="steps" aria-label="Shutdown steps, read from the event log">
				<li class="done"><span class="mark">✓</span>Stop Minecraft</li>
				<li class={saved ? 'done' : 'active'}>
					<span class="mark">{saved ? '✓' : '…'}</span>Upload the world to R2
				</li>
				<li class={saved ? 'active' : ''}><span class="mark">{saved ? '…' : '·'}</span>Delete the server</li>
			</ol>
			<p class="sub">Nothing is deleted until the world is confirmed in R2.</p>
		</div>
	{:else if s.state === 'ERROR'}
		<div class="copy">
			<p class="eyebrow">Needs a human</p>
			<h2 id="hero-title">{hasServer ? 'The world has not been saved' : 'Something went wrong'}</h2>
			<p class="what" role="alert">{s.lastError || 'No error was recorded.'}</p>
			{#if hasServer}
				<p class="sub">
					<strong>The server is still running, on purpose.</strong> Deleting it now would delete the
					world with it, and it is billing about {euros(s.hourlyRate, 4)} an hour in the meantime.
					Retrying the save is safe. From a shell: <code>outpost stop</code>.
				</p>
			{/if}
		</div>
	{/if}

	<div class="actions">
		{#if canStart}
			<button class="btn primary big" onclick={onstart} disabled={pending !== null} aria-busy={pending === 'start'}>
				{pending === 'start' ? 'Renting…' : 'Start server'}
			</button>
		{/if}
		{#if canStop}
			<button
				class="btn danger"
				class:armed={confirmStop}
				onclick={onstop}
				disabled={pending !== null}
				aria-busy={pending === 'stop'}
			>
				{#if pending === 'stop'}
					Saving…
				{:else if confirmStop}
					Really? Tap again
				{:else if s.state === 'ERROR'}
					Retry the save
				{:else if s.state === 'PROVISIONING'}
					Cancel
				{:else}
					Save and stop
				{/if}
			</button>
		{/if}
	</div>

	{#if ['RUNNING', 'IDLE', 'PROVISIONING'].includes(s.state)}
		<dl class="facts">
			<div><dt>this session</dt><dd class="num">{euros(sessionCost)}</dd></div>
			<div><dt>uptime</dt><dd class="num">{duration(uptime)}</dd></div>
			{#if s.state !== 'PROVISIONING'}
				<div><dt>address</dt><dd class="mono">{s.dnsName}</dd></div>
			{/if}
		</dl>
	{/if}
</section>

<style>
	.hero {
		display: grid;
		grid-template-columns: minmax(0, 1fr);
		gap: 1.1rem;
		background: linear-gradient(180deg, var(--panel-2), var(--panel));
		border: 1px solid var(--line);
		border-radius: var(--radius);
		padding: 1.4rem 1.3rem;
		/* the first paint and the first poll must not move the page */
		min-height: 15rem;
		align-content: space-between;
	}
	.hero.error {
		border-color: color-mix(in srgb, var(--clay) 55%, transparent);
		background: linear-gradient(180deg, color-mix(in srgb, var(--clay) 12%, var(--panel-2)), var(--panel));
	}
	.hero.offline {
		background: radial-gradient(500px 220px at 20% 0%, color-mix(in srgb, var(--moss) 8%, transparent), transparent),
			linear-gradient(180deg, var(--panel-2), var(--panel));
	}
	.eyebrow {
		margin: 0 0 0.35rem;
		font-size: 0.74rem;
		letter-spacing: 0.09em;
		text-transform: uppercase;
		color: var(--faint);
		font-weight: 650;
	}
	.hero.error .eyebrow {
		color: var(--clay);
	}
	h2 {
		font-size: 1.7rem;
		font-weight: 600;
		line-height: 1.2;
		margin: 0 0 0.5rem;
	}
	.clock {
		color: var(--moss);
	}
	.big-count .n {
		font-size: 2.6rem;
		color: var(--moss);
	}
	.sub {
		margin: 0.5rem 0 0;
		color: var(--dim);
		font-size: 0.93rem;
		max-width: 38rem;
	}
	.what {
		margin: 0 0 0.4rem;
		font-family: var(--mono);
		font-size: 0.85rem;
		color: var(--clay);
		overflow-wrap: anywhere;
		background: color-mix(in srgb, var(--clay) 8%, transparent);
		border-left: 3px solid var(--clay-deep);
		border-radius: 6px;
		padding: 0.6rem 0.75rem;
	}
	.safe {
		margin: 0.9rem 0 0;
		font-size: 0.88rem;
		color: var(--dim);
	}
	.dot {
		display: inline-block;
		width: 8px;
		height: 8px;
		border-radius: 50%;
		background: var(--slate);
		margin-right: 0.45rem;
	}
	.dot.ok {
		background: var(--moss);
	}
	.names {
		list-style: none;
		display: flex;
		flex-wrap: wrap;
		gap: 0.4rem;
		margin: 0.2rem 0 0;
		padding: 0;
	}
	.track {
		position: relative;
		height: 10px;
		border-radius: 999px;
		background: var(--bg-glow);
		border: 1px solid var(--line);
		margin: 0.6rem 0 0.35rem;
	}
	.track i {
		position: absolute;
		inset: 0 auto 0 0;
		border-radius: 999px;
		background: linear-gradient(90deg, var(--amber), var(--moss));
		transition: width 900ms linear;
	}
	.track.drain i {
		background: linear-gradient(90deg, var(--clay-deep), var(--amber));
	}
	.track b {
		position: absolute;
		top: -4px;
		bottom: -4px;
		width: 2px;
		background: var(--faint);
	}
	.scale {
		display: flex;
		justify-content: space-between;
		margin: 0;
		font-size: 0.78rem;
		color: var(--faint);
	}
	.steps {
		list-style: none;
		margin: 0.4rem 0 0;
		padding: 0;
		display: grid;
		gap: 0.45rem;
	}
	.steps li {
		display: flex;
		gap: 0.7rem;
		align-items: center;
		color: var(--faint);
	}
	.steps li.done {
		color: var(--dim);
	}
	.steps li.active {
		color: var(--text);
		font-weight: 600;
	}
	.mark {
		width: 1.4rem;
		text-align: center;
		font-family: var(--mono);
		color: var(--moss);
	}
	.actions {
		display: flex;
		flex-wrap: wrap;
		gap: 0.6rem;
	}
	.big {
		font-size: 1.05rem;
		padding-inline: 1.8rem;
		min-height: 52px;
	}
	.facts {
		display: flex;
		flex-wrap: wrap;
		gap: 0.4rem 2rem;
		margin: 0;
		padding-top: 0.9rem;
		border-top: 1px solid var(--line-soft);
	}
	.facts div {
		display: flex;
		gap: 0.6rem;
		align-items: baseline;
	}
	dt {
		color: var(--faint);
		font-size: 0.82rem;
	}
	dd {
		margin: 0;
		color: var(--text);
	}
	@media (max-width: 30rem) {
		h2 {
			font-size: 1.4rem;
		}
		.actions .btn {
			flex: 1 1 100%;
		}
	}
</style>

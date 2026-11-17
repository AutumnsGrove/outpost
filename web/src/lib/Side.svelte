<script lang="ts">
	import type { Component } from 'svelte';
	import Ban from '@lucide/svelte/icons/ban';
	import Check from '@lucide/svelte/icons/check';
	import Circle from '@lucide/svelte/icons/circle';
	import CircleCheck from '@lucide/svelte/icons/circle-check';
	import CircleHelp from '@lucide/svelte/icons/circle-help';
	import CircleSlash from '@lucide/svelte/icons/circle-slash';
	import Clock from '@lucide/svelte/icons/clock';
	import Globe from '@lucide/svelte/icons/globe';
	import HardDrive from '@lucide/svelte/icons/hard-drive';
	import History from '@lucide/svelte/icons/history';
	import LinkIcon from '@lucide/svelte/icons/link';
	import Minus from '@lucide/svelte/icons/minus';
	import Moon from '@lucide/svelte/icons/moon';
	import Play from '@lucide/svelte/icons/play';
	import Power from '@lucide/svelte/icons/power';
	import Receipt from '@lucide/svelte/icons/receipt';
	import Save from '@lucide/svelte/icons/save';
	import Shuffle from '@lucide/svelte/icons/shuffle';
	import Terminal from '@lucide/svelte/icons/terminal';
	import TimerOff from '@lucide/svelte/icons/timer-off';
	import Trash from '@lucide/svelte/icons/trash-2';
	import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
	import Unplug from '@lucide/svelte/icons/unplug';
	import UserCheck from '@lucide/svelte/icons/user-round-check';
	import UserRound from '@lucide/svelte/icons/user-round';
	import UserX from '@lucide/svelte/icons/user-round-x';
	import { ago, bytes, clock, duration, euros, type Overview } from './api';

	let { o, now }: { o: Overview; now: number } = $props();

	// One glyph per event kind, so a row reads at a glance. The kind itself stays
	// in the markup for screen readers; unknown kinds get a plain dot rather than
	// a blank column.
	const eventIcon: Record<string, Component> = {
		start: Play,
		stop: Power,
		ready: CircleCheck,
		saved: Save,
		'save-failed': TriangleAlert,
		'no-world': CircleSlash,
		'rcon-dead': Unplug,
		'provision-timeout': TimerOff,
		idle: Moon,
		'idle-limit': TimerOff,
		empty: UserX,
		resume: UserCheck,
		player: UserRound,
		session: Receipt,
		console: Terminal,
		'console-refused': Ban,
		dns: Globe,
		'dns-error': Globe,
		deleted: Trash,
		reset: Trash,
		fallback: Shuffle,
		'no-capacity': Ban,
		adopted: LinkIcon,
		crash: TriangleAlert,
		why: CircleHelp,
		error: TriangleAlert
	};

	const dnsText = $derived(
		{
			ok: 'points at the live server',
			pending: 'live server is up; record has not caught up',
			idle: 'still points at the last server — a dead address fails fast, by design',
			unset: 'no record published yet'
		}[o.dns.state]
	);
</script>

<section class="card" aria-labelledby="world-h">
	<h2 id="world-h"><HardDrive aria-hidden="true" />World &amp; address</h2>
	{#if o.backups.length === 0}
		<p class="muted">No backup yet. The first one is taken when the first session ends.</p>
	{:else}
		<p class="lead">
			<span class="num">{bytes(o.backups[0].sizeBytes)}</span> saved
			<strong>{ago(o.backups[0].at, now)}</strong>
		</p>
		<ul class="list">
			{#each o.backups.slice(1, 4) as b (b.id)}
				<li><span class="muted">{clock(b.at)}</span><span class="num">{bytes(b.sizeBytes)}</span></li>
			{/each}
		</ul>
	{/if}
	<p class="dns">
		<span class="state {o.dns.state}" aria-hidden="true">
			<Globe />
			{#if o.dns.state === 'ok'}<Check />{:else if o.dns.state === 'pending'}<Clock />{:else}<Minus />{/if}
		</span>
		<span>
			<span class="mono">{o.dns.name}</span>
			{#if o.dns.pointsAt}→ <span class="mono">{o.dns.pointsAt}</span>{/if}
			<span class="muted dnsnote">{dnsText}</span>
		</span>
	</p>
</section>

<section class="card" aria-labelledby="ledger-h">
	<h2 id="ledger-h"><Receipt aria-hidden="true" />Ledger</h2>
	<p class="lead">
		<span class="num">{euros(o.totals.cost)}</span>
		<span class="muted"
			>over {o.totals.sessions} session{o.totals.sessions === 1 ? '' : 's'}, {o.totals.hours.toFixed(2)} h</span
		>
	</p>
	{#if o.sessions.filter((s) => s.endedAt).length === 0}
		<p class="muted">No finished sessions yet. Each one lands here with its cost.</p>
	{:else}
		<table>
			<caption class="sr">Recent sessions</caption>
			<thead>
				<tr><th scope="col">When</th><th scope="col">Length</th><th scope="col">Peak</th><th scope="col">Cost</th></tr>
			</thead>
			<tbody>
				{#each o.sessions.filter((s) => s.endedAt).slice(0, 5) as s (s.id)}
					<tr>
						<td class="muted">{clock(s.startedAt)}</td>
						<td class="num">{duration(s.durationSeconds)}</td>
						<td class="num">{s.maxPlayers}</td>
						<td class="num">{euros(s.cost)}</td>
					</tr>
				{/each}
			</tbody>
		</table>
	{/if}
</section>

<section class="card" aria-labelledby="events-h">
	<h2 id="events-h"><History aria-hidden="true" />Recent</h2>
	{#if o.events.length === 0}
		<p class="muted">Nothing has happened yet.</p>
	{:else}
		<ul class="events">
			{#each o.events.slice(0, 7) as e (e.id)}
				{@const Icon = eventIcon[e.kind] ?? Circle}
				<li>
					<span class="when num">{clock(e.at)}</span>
					<span class="kind" data-kind={e.kind} title={e.kind}>
						<Icon aria-hidden="true" />
						<span class="sr">{e.kind}</span>
					</span>
					<span class="what">{e.message}</span>
				</li>
			{/each}
		</ul>
	{/if}
</section>

<style>
	.lead {
		margin: 0 0 0.6rem;
		font-size: 1.05rem;
	}
	.lead .num {
		font-size: 1.35rem;
	}
	.list {
		list-style: none;
		margin: 0 0 0.7rem;
		padding: 0;
		font-size: 0.85rem;
		display: grid;
		gap: 0.15rem;
	}
	.list li {
		display: flex;
		justify-content: space-between;
	}
	.dns {
		display: flex;
		gap: 0.6rem;
		align-items: flex-start;
		margin: 0.7rem 0 0;
		padding-top: 0.7rem;
		border-top: 1px solid var(--line-soft);
		font-size: 0.85rem;
		overflow-wrap: anywhere;
	}
	.dnsnote {
		display: block;
		font-size: 0.78rem;
	}
	.state {
		display: inline-flex;
		align-items: center;
		gap: 0.25rem;
		margin-top: 0.2rem;
		color: var(--faint);
		flex: none;
	}
	.state.ok {
		color: var(--moss);
	}
	.state.pending {
		color: var(--amber);
	}
	table {
		width: 100%;
		border-collapse: collapse;
		font-size: 0.84rem;
	}
	th {
		text-align: left;
		font-weight: 600;
		color: var(--faint);
		font-size: 0.72rem;
		letter-spacing: 0.06em;
		text-transform: uppercase;
		padding-bottom: 0.3rem;
	}
	td {
		padding: 0.22rem 0;
		border-top: 1px solid var(--line-soft);
	}
	td:last-child,
	th:last-child {
		text-align: right;
	}
	.events {
		list-style: none;
		margin: 0;
		padding: 0;
		display: grid;
		gap: 0.35rem;
	}
	.events li {
		display: grid;
		grid-template-columns: auto 1.1rem minmax(0, 1fr);
		gap: 0.5rem;
		font-size: 0.82rem;
		color: var(--dim);
	}
	.when {
		color: var(--faint);
		white-space: nowrap;
	}
	.kind {
		display: inline-flex;
		align-items: center;
		align-self: start;
		margin-top: 0.12rem;
		color: var(--moss-dim);
	}
	.kind[data-kind='error'],
	.kind[data-kind='crash'],
	.kind[data-kind='save-failed'],
	.kind[data-kind='rcon-dead'],
	.kind[data-kind='console-refused'] {
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
</style>

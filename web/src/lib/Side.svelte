<script lang="ts">
	import { ago, bytes, clock, duration, euros, type Overview } from './api';

	let { o, now }: { o: Overview; now: number } = $props();

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
	<h2 id="world-h">World &amp; address</h2>
	{#if o.backups.length === 0}
		<p class="muted">No backup yet. The first one is taken when the first session ends.</p>
	{:else}
		<p class="lead">
			<span class="num">{bytes(o.backups[0].sizeBytes)}</span> saved
			<strong>{ago(o.backups[0].at, now)}</strong>
		</p>
		<ul class="list">
			{#each o.backups.slice(1, 4) as b (b.at)}
				<li><span class="muted">{clock(b.at)}</span><span class="num">{bytes(b.sizeBytes)}</span></li>
			{/each}
		</ul>
	{/if}
	<p class="dns">
		<span class="state {o.dns.state}" aria-hidden="true"></span>
		<span>
			<span class="mono">{o.dns.name}</span>
			{#if o.dns.pointsAt}→ <span class="mono">{o.dns.pointsAt}</span>{/if}
			<span class="muted dnsnote">{dnsText}</span>
		</span>
	</p>
</section>

<section class="card" aria-labelledby="ledger-h">
	<h2 id="ledger-h">Ledger</h2>
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
	<h2 id="events-h">Recent</h2>
	{#if o.events.length === 0}
		<p class="muted">Nothing has happened yet.</p>
	{:else}
		<ul class="events">
			{#each o.events.slice(0, 7) as e (e.id)}
				<li>
					<span class="when num">{clock(e.at)}</span>
					<span class="kind" data-kind={e.kind}>{e.kind}</span>
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
		width: 9px;
		height: 9px;
		border-radius: 50%;
		margin-top: 0.45rem;
		flex: none;
		background: var(--slate);
	}
	.state.ok {
		background: var(--moss);
	}
	.state.pending {
		background: var(--amber);
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
		grid-template-columns: 4.2rem 5.6rem 1fr;
		gap: 0.5rem;
		font-size: 0.82rem;
		color: var(--dim);
	}
	.when {
		color: var(--faint);
		white-space: nowrap;
	}
	.kind {
		font-size: 0.7rem;
		letter-spacing: 0.05em;
		text-transform: uppercase;
		color: var(--moss-dim);
		overflow-wrap: anywhere;
	}
	.kind[data-kind='error'],
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

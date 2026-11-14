<script lang="ts">
	import { playerAction, type PlayerAction, type Players } from './api';

	let {
		players,
		token,
		onchanged
	}: { players: Players; token: string; onchanged: () => void } = $props();

	let name = $state('');
	let busy = $state(false);
	let message = $state('');
	let failed = $state(false);
	let armed = $state<string | null>(null);
	let armTimer: ReturnType<typeof setTimeout>;

	const ops = $derived(new Set(players.ops.map((n) => n.toLowerCase())));
	const online = $derived(new Set(players.online.map((n) => n.toLowerCase())));

	async function run(action: PlayerAction, who: string) {
		if (busy) return;
		busy = true;
		message = '';
		try {
			const res = await playerAction(action, who, token);
			message = res.message;
			failed = false;
			if (action === 'add') name = '';
			onchanged();
		} catch (err) {
			message = err instanceof Error ? err.message : String(err);
			failed = true;
		} finally {
			busy = false;
			armed = null;
		}
	}

	function remove(who: string) {
		if (armed === who) return run('remove', who);
		armed = who;
		clearTimeout(armTimer);
		armTimer = setTimeout(() => (armed = null), 4000);
	}
</script>

<section class="card" aria-labelledby="players-h">
	<h2 id="players-h">Players</h2>

	{#if !players.roster}
		<p class="muted">
			{players.rosterError
				? `Could not read the whitelist from R2: ${players.rosterError}`
				: 'The player list needs R2 credentials, which this daemon was started without.'}
		</p>
	{:else}
		{#if players.whitelist.length === 0}
			<p class="muted">Nobody can join yet. Add a Minecraft name below.</p>
		{:else}
			<ul class="rows">
				{#each players.whitelist as who (who)}
					<li>
						<span class="who">
							<span class="dot" class:on={online.has(who.toLowerCase())} title={online.has(who.toLowerCase()) ? 'online' : 'offline'}></span>
							<span class="nm">{who}</span>
							{#if ops.has(who.toLowerCase())}<span class="badge">op</span>{/if}
						</span>
						<span class="btns">
							<button
								class="btn quiet small"
								disabled={busy}
								onclick={() => run(ops.has(who.toLowerCase()) ? 'deop' : 'op', who)}
								aria-label="{ops.has(who.toLowerCase()) ? 'Remove operator from' : 'Make operator'} {who}"
							>
								{ops.has(who.toLowerCase()) ? 'De-op' : 'Op'}
							</button>
							<button
								class="btn danger small"
								class:armed={armed === who}
								disabled={busy}
								onclick={() => remove(who)}
								aria-label="Remove {who} from the whitelist"
							>
								{armed === who ? 'Sure?' : 'Remove'}
							</button>
						</span>
					</li>
				{/each}
			</ul>
		{/if}

		<form
			onsubmit={(e) => {
				e.preventDefault();
				if (name.trim()) run('add', name.trim());
			}}
		>
			<label class="sr" for="add-name">Minecraft name to whitelist</label>
			<input
				id="add-name"
				class="field"
				placeholder="Minecraft name"
				bind:value={name}
				autocomplete="off"
				autocapitalize="off"
				spellcheck="false"
				maxlength="16"
			/>
			<button class="btn primary small" disabled={busy || !name.trim()} aria-busy={busy}>
				{busy ? 'Working…' : 'Whitelist'}
			</button>
		</form>
		<p class="status" class:bad={failed} role="status" aria-live="polite">{message}</p>
		<p class="muted note">
			Saved to R2, so it survives the server being deleted; applied live when one is running.
		</p>
	{/if}
</section>

<style>
	.rows {
		list-style: none;
		margin: 0 0 0.9rem;
		padding: 0;
		display: grid;
	}
	.rows li {
		display: flex;
		justify-content: space-between;
		align-items: center;
		gap: 0.6rem;
		padding: 0.3rem 0;
		border-bottom: 1px solid var(--line-soft);
	}
	.rows li:last-child {
		border-bottom: 0;
	}
	.who {
		display: flex;
		align-items: center;
		gap: 0.55rem;
		min-width: 0;
	}
	.nm {
		overflow-wrap: anywhere;
	}
	.dot {
		width: 9px;
		height: 9px;
		border-radius: 50%;
		background: var(--line);
		flex: none;
	}
	.dot.on {
		background: var(--moss);
		box-shadow: 0 0 0 3px color-mix(in srgb, var(--moss) 25%, transparent);
	}
	.btns {
		display: flex;
		gap: 0.35rem;
		flex: none;
	}
	form {
		display: flex;
		gap: 0.5rem;
	}
	form .field {
		flex: 1;
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
	}
</style>

<script lang="ts">
	import { tick } from 'svelte';
	import { sendConsole } from './api';

	let { live, token }: { live: boolean; token: string } = $props();

	type Entry = { cmd: string; out: string; bad: boolean };
	let input = $state('');
	let busy = $state(false);
	let entries = $state<Entry[]>([]);
	let recall = $state(-1);
	let log: HTMLElement | undefined = $state();
	let field: HTMLInputElement | undefined = $state();

	const quick = ['list', 'time set day', 'weather clear', 'save-all'];

	async function send(cmd = input) {
		cmd = cmd.trim();
		if (!cmd || busy || !live) return;
		busy = true;
		input = '';
		recall = -1;
		try {
			const r = await sendConsole(cmd, token);
			entries = [...entries.slice(-49), { cmd, out: r.output || '(no output)', bad: false }];
		} catch (err) {
			entries = [
				...entries.slice(-49),
				{ cmd, out: err instanceof Error ? err.message : String(err), bad: true }
			];
		} finally {
			busy = false;
			await tick();
			if (log) log.scrollTop = log.scrollHeight;
			field?.focus();
		}
	}

	function keydown(e: KeyboardEvent) {
		// Up/Down walk back through what was typed, like a shell.
		if (e.key !== 'ArrowUp' && e.key !== 'ArrowDown') return;
		const sent = entries.map((x) => x.cmd);
		if (!sent.length) return;
		e.preventDefault();
		recall = e.key === 'ArrowUp' ? Math.min(recall + 1, sent.length - 1) : Math.max(recall - 1, -1);
		input = recall < 0 ? '' : sent[sent.length - 1 - recall];
	}
</script>

<section class="card" aria-labelledby="console-h">
	<h2 id="console-h">Console</h2>

	<div class="log mono" class:idle={!live} bind:this={log} role="log" aria-live="polite" aria-label="Console output">
		{#if entries.length === 0}
			<p class="empty">
				{live
					? 'Every vanilla command works here: op, kick, time set day, difficulty. Output appears below.'
					: 'The console opens once the server is running.'}
			</p>
		{/if}
		{#each entries as e, i (i)}
			<div class="entry">
				<div class="cmd"><span aria-hidden="true">›</span> {e.cmd}</div>
				<pre class:bad={e.bad}>{e.out}</pre>
			</div>
		{/each}
	</div>

	<form
		onsubmit={(e) => {
			e.preventDefault();
			send();
		}}
	>
		<label class="sr" for="cmd">Minecraft command</label>
		<input
			id="cmd"
			class="field mono"
			placeholder={live ? 'list' : 'server is not running'}
			bind:value={input}
			bind:this={field}
			onkeydown={keydown}
			disabled={!live}
			autocomplete="off"
			autocapitalize="off"
			autocorrect="off"
			spellcheck="false"
			enterkeyhint="send"
		/>
		<button class="btn primary small" disabled={!live || busy || !input.trim()} aria-busy={busy}>
			{busy ? '…' : 'Send'}
		</button>
	</form>

	<div class="quick" aria-label="Quick commands">
		{#each quick as q (q)}
			<button
				class="btn quiet small chipbtn mono"
				disabled={!live || busy}
				onclick={() => (q === 'list' ? send(q) : ((input = q), field?.focus()))}>{q}</button
			>
		{/each}
	</div>
	<p class="muted note">
		<code>stop</code> and <code>save-off</code> are refused — use Save and stop. Every command is
		logged.
	</p>
</section>

<style>
	.log {
		background: #100e0b;
		border: 1px solid var(--line-soft);
		border-radius: 10px;
		padding: 0.7rem 0.8rem;
		height: 13rem;
		overflow-y: auto;
		font-size: 0.82rem;
		margin-bottom: 0.6rem;
	}
	.log.idle {
		height: 4.5rem;
	}
	.empty {
		margin: 0;
		color: var(--faint);
		font-family: var(--sans);
		font-size: 0.86rem;
	}
	.entry + .entry {
		margin-top: 0.55rem;
	}
	.cmd {
		color: var(--moss);
	}
	pre {
		margin: 0.15rem 0 0;
		white-space: pre-wrap;
		overflow-wrap: anywhere;
		color: var(--dim);
		font: inherit;
	}
	pre.bad {
		color: var(--clay);
	}
	form {
		display: flex;
		gap: 0.5rem;
	}
	form .field {
		flex: 1;
	}
	.quick {
		display: flex;
		flex-wrap: wrap;
		gap: 0.4rem;
		margin-top: 0.55rem;
	}
	.chipbtn {
		border-radius: 999px;
		font-size: 0.8rem;
	}
	.note {
		font-size: 0.78rem;
		margin-top: 0.6rem;
	}
</style>

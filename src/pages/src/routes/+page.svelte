<script lang="ts">
	import { onMount } from 'svelte';

	type ServerState = 'OFFLINE' | 'PROVISIONING' | 'RUNNING' | 'IDLE' | 'SUSPENDED' | 'TERMINATING';

	interface Status {
		state: ServerState;
		players: { online: number; max: number };
		version: string;
	}

	let status = $state<Status | null>(null);
	let error = $state<string | null>(null);
	let loading = $state(true);

	const stateColors: Record<ServerState, string> = {
		OFFLINE: '#6b7280',
		PROVISIONING: '#f59e0b',
		RUNNING: '#22c55e',
		IDLE: '#eab308',
		SUSPENDED: '#f97316',
		TERMINATING: '#ef4444'
	};

	const stateEmoji: Record<ServerState, string> = {
		OFFLINE: '⚫',
		PROVISIONING: '🔄',
		RUNNING: '🟢',
		IDLE: '🟡',
		SUSPENDED: '🟠',
		TERMINATING: '🔴'
	};

	async function fetchStatus() {
		try {
			// TODO: Replace with actual API endpoint
			const res = await fetch('https://admin.grove.place/api/mc/status/public');
			if (!res.ok) throw new Error('Failed to fetch status');
			status = await res.json();
			error = null;
		} catch (e) {
			error = 'Unable to fetch server status';
			// Default offline status
			status = { state: 'OFFLINE', players: { online: 0, max: 20 }, version: '1.20.1' };
		} finally {
			loading = false;
		}
	}

	onMount(() => {
		fetchStatus();
		// Refresh every 30 seconds
		const interval = setInterval(fetchStatus, 30000);
		return () => clearInterval(interval);
	});
</script>

<svelte:head>
	<title>Grove Minecraft Server</title>
	<meta name="description" content="Status page for the Grove Minecraft server" />
</svelte:head>

<main>
	<div class="container">
		<h1>Grove Minecraft Server</h1>

		{#if loading}
			<div class="status-box loading">
				<p>Loading...</p>
			</div>
		{:else if status}
			<div class="status-box" style="border-color: {stateColors[status.state]}">
				<div class="status-row">
					<span class="label">Status:</span>
					<span class="value">{stateEmoji[status.state]} {status.state}</span>
				</div>
				<div class="status-row">
					<span class="label">Players:</span>
					<span class="value">{status.players.online} / {status.players.max}</span>
				</div>
				<div class="status-row">
					<span class="label">Version:</span>
					<span class="value">{status.version} (Fabric)</span>
				</div>
			</div>

			{#if status.state === 'RUNNING' || status.state === 'IDLE'}
				<div class="connect-info">
					<p><strong>Connect:</strong> grove.place</p>
				</div>

				<a href="https://map.grove.place" class="map-link" target="_blank" rel="noopener">
					View Live Map
				</a>
			{/if}
		{/if}

		<hr />

		<p class="notice">
			This is a private, whitelisted server.<br />
			Contact Autumn for access.
		</p>
	</div>
</main>

<style>
	:global(body) {
		margin: 0;
		font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, Oxygen, Ubuntu, sans-serif;
		background: #1a1a1a;
		color: #e5e5e5;
	}

	main {
		min-height: 100vh;
		display: flex;
		align-items: center;
		justify-content: center;
		padding: 2rem;
	}

	.container {
		max-width: 400px;
		text-align: center;
	}

	h1 {
		color: #22c55e;
		margin-bottom: 2rem;
	}

	.status-box {
		background: #262626;
		border: 2px solid #404040;
		border-radius: 8px;
		padding: 1.5rem;
		margin-bottom: 1.5rem;
	}

	.status-box.loading {
		color: #9ca3af;
	}

	.status-row {
		display: flex;
		justify-content: space-between;
		padding: 0.5rem 0;
	}

	.label {
		color: #9ca3af;
	}

	.value {
		font-weight: 600;
	}

	.connect-info {
		background: #262626;
		border-radius: 8px;
		padding: 1rem;
		margin-bottom: 1rem;
	}

	.connect-info p {
		margin: 0;
	}

	.map-link {
		display: inline-block;
		background: #22c55e;
		color: #000;
		padding: 0.75rem 1.5rem;
		border-radius: 6px;
		text-decoration: none;
		font-weight: 600;
		transition: background 0.2s;
	}

	.map-link:hover {
		background: #16a34a;
	}

	hr {
		border: none;
		border-top: 1px solid #404040;
		margin: 2rem 0;
	}

	.notice {
		color: #9ca3af;
		font-size: 0.9rem;
	}
</style>

export type ServerState =
	| 'OFFLINE'
	| 'PROVISIONING'
	| 'RUNNING'
	| 'IDLE'
	| 'TERMINATING'
	| 'ERROR';

/** /api/status — a contract with Meridian's tile; new data goes in Overview. */
export interface Status {
	state: ServerState;
	region: string;
	serverType: string;
	serverId: string;
	serverIp: string;
	hostname: string;
	players: number;
	uptimeSeconds: number;
	idleSeconds: number;
	ttlSeconds: number;
	startedAt: string;
	hourlyRate: number;
	sessionCost: number;
	totalHours: number;
	totalCost: number;
	lastError: string;
	dnsName: string;
	idleGraceSeconds: number;
	idleLimitSeconds: number;
	formToken: string;
}

export interface Health {
	rconFailures: number;
	rconFailureLimit: number;
	everRunning: boolean;
	provisionTimeoutSeconds: number;
	lastDns: string;
}

export interface Event {
	id: number;
	at: string;
	kind: string;
	message: string;
}

export interface Session {
	id: number;
	startedAt: string;
	endedAt: string;
	durationSeconds: number;
	cost: number;
	maxPlayers: number;
	region: string;
	serverType: string;
}

export interface Backup {
	at: string;
	sizeBytes: number;
	kind: string;
}

export interface Dns {
	name: string;
	pointsAt: string;
	serverIp: string;
	state: 'ok' | 'pending' | 'idle' | 'unset';
}

export interface Players {
	online: string[];
	roster: boolean;
	whitelist: string[];
	ops: string[];
	rosterError?: string;
}

export interface Overview {
	status: Status;
	health: Health;
	events: Event[];
	sessions: Session[];
	totals: { hours: number; cost: number; sessions: number };
	backups: Backup[];
	dns: Dns;
	players: Players;
	build: { commit: string; built: string; modified: boolean };
	canUpdate: boolean;
}

export type ActionKind = 'start' | 'stop' | 'update';
export type PlayerAction = 'add' | 'remove' | 'op' | 'deop';

async function json<T>(res: Response): Promise<T> {
	if (!res.ok) {
		let message = `${res.status} ${res.statusText}`;
		try {
			const body = await res.json();
			if (body?.error) message = body.error;
		} catch {
			/* not JSON, keep the status line */
		}
		throw new Error(message);
	}
	return (await res.json()) as T;
}

function post<T>(path: string, body: unknown): Promise<T> {
	return fetch(path, {
		method: 'POST',
		headers: { 'content-type': 'application/json' },
		body: JSON.stringify(body)
	}).then((r) => json<T>(r));
}

export async function getOverview(): Promise<Overview> {
	return json<Overview>(await fetch('/api/overview', { headers: { accept: 'application/json' } }));
}

export const act = (action: ActionKind, token: string) =>
	post<{ message?: string }>('/api/action', { action, token });

export const sendConsole = (command: string, token: string) =>
	post<{ output: string; truncated: boolean }>('/api/console', { command, token });

export const playerAction = (action: PlayerAction, name: string, token: string) =>
	post<{ name: string; message: string; applied: boolean }>('/api/players', {
		action,
		name,
		token
	});

export function euros(n: number, places = 4): string {
	return '€' + n.toFixed(places);
}

/** "4m 12s", "1h 03m", "38s" — for uptimes and countdowns. */
export function duration(seconds: number): string {
	if (!seconds || seconds < 0) return '0s';
	seconds = Math.round(seconds);
	if (seconds < 60) return `${seconds}s`;
	const m = Math.floor(seconds / 60);
	const s = seconds % 60;
	if (m < 60) return s ? `${m}m ${String(s).padStart(2, '0')}s` : `${m}m`;
	const h = Math.floor(m / 60);
	return `${h}h ${String(m % 60).padStart(2, '0')}m`;
}

/** "02:10" — a running clock. */
export function mmss(seconds: number): string {
	seconds = Math.max(0, Math.round(seconds));
	return `${String(Math.floor(seconds / 60)).padStart(2, '0')}:${String(seconds % 60).padStart(2, '0')}`;
}

export function clock(iso: string): string {
	if (!iso) return '';
	const d = new Date(iso);
	if (Number.isNaN(d.getTime())) return '';
	const sameDay = d.toDateString() === new Date().toDateString();
	const time = d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
	return sameDay ? time : `${d.toLocaleDateString([], { month: 'short', day: 'numeric' })} ${time}`;
}

export function ago(iso: string, now = Date.now()): string {
	const t = new Date(iso).getTime();
	if (!iso || Number.isNaN(t)) return '';
	const s = Math.max(0, (now - t) / 1000);
	if (s < 60) return 'just now';
	if (s < 3600) return `${Math.floor(s / 60)} min ago`;
	if (s < 86400) return `${Math.floor(s / 3600)} h ago`;
	return `${Math.floor(s / 86400)} d ago`;
}

export function bytes(n: number): string {
	if (n < 1024) return `${n} B`;
	const units = ['KB', 'MB', 'GB'];
	let v = n;
	for (const u of units) {
		v /= 1024;
		if (v < 1024) return `${v.toFixed(1)} ${u}`;
	}
	return `${v.toFixed(1)} TB`;
}

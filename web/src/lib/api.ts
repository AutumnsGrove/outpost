export type ServerState =
	| 'OFFLINE'
	| 'PROVISIONING'
	| 'RUNNING'
	| 'IDLE'
	| 'TERMINATING'
	| 'ERROR';

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

export interface Event {
	at: string;
	kind: string;
	message: string;
}

export type ActionKind = 'start' | 'stop' | 'update';

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

export async function getStatus(): Promise<Status> {
	return json<Status>(await fetch('/api/status', { headers: { accept: 'application/json' } }));
}

export async function getEvents(): Promise<{ events: Event[] }> {
	return json<{ events: Event[] }>(
		await fetch('/api/events', { headers: { accept: 'application/json' } })
	);
}

export async function act(action: ActionKind, token: string): Promise<{ message?: string }> {
	return json<{ message?: string }>(
		await fetch('/api/action', {
			method: 'POST',
			headers: { 'content-type': 'application/json' },
			body: JSON.stringify({ action, token })
		})
	);
}

export function euros(n: number, places = 4): string {
	return '€' + n.toFixed(places);
}

/** "4m 12s", "1h 03m", "38s" — for uptimes and countdowns. */
export function duration(seconds: number): string {
	if (!seconds || seconds < 0) return '';
	if (seconds < 60) return `${Math.round(seconds)}s`;
	const m = Math.floor(seconds / 60);
	const s = Math.round(seconds % 60);
	if (m < 60) return s ? `${m}m ${String(s).padStart(2, '0')}s` : `${m}m`;
	const h = Math.floor(m / 60);
	return `${h}h ${String(m % 60).padStart(2, '0')}m`;
}

export function clock(iso: string): string {
	if (!iso) return '';
	const d = new Date(iso);
	if (Number.isNaN(d.getTime())) return '';
	const now = new Date();
	const sameDay = d.toDateString() === now.toDateString();
	const time = d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
	return sameDay ? time : `${d.toLocaleDateString([], { month: 'short', day: 'numeric' })} ${time}`;
}

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
	id: number;
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

/** One rentable size, priced the way the picker shows it. */
export interface Plan {
	type: string;
	vcpu: number;
	ramGb: number;
	/** EUR per hour, list price including the IPv4 a server needs. */
	hourlyRate: number;
	arm: boolean;
	/** Enough RAM for the active pack. */
	fits: boolean;
	/** False for a configured plan this build cannot price. */
	known: boolean;
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
	/** Whether a host update watcher is installed, so the button means something. */
	canUpdate: boolean;
	/** What the watcher last said. Absent when there is no watcher. */
	update?: UpdateStatus;
	/** What Start can rent, smallest first, and the plan it opens on. */
	plans: Plan[];
	selectedType: string;
	/** What the active pack asks for; 0 when it has no manifest figure. */
	packMemoryGb: number;
}

/** The update mailbox between the daemon and the host watcher (updatesignal). */
export interface UpdateStatus {
	configured: boolean;
	dir?: string;
	available?: {
		current: string;
		latest: string;
		latestDigest: string;
		checkedAt: string;
		updateAvailable: boolean;
	};
	result?: {
		status: 'ok' | 'failed' | 'skipped';
		detail: string;
		target?: string;
		finishedAt?: string;
	};
	heartbeat?: string;
}

/** A pack as R2 records it: what was installed, and what it needs to run. */
export interface PackMeta {
	source: string;
	slug: string;
	name: string;
	versionId: string;
	versionNumber: string;
	mc: string;
	loader: string;
	loaderVersion: string;
	fileCount: number;
	memoryGB: number;
	fetchedAt: string;
}

export interface PackFile {
	path: string;
	size: number;
	source: 'pack' | 'override';
}

export interface PackJobResult {
	pack: PackMeta;
	profile: string;
	mods: PackFile[];
	files: number;
	bytes: number;
	clientOnly?: string[];
	warnings?: string[];
	worldPack?: PackMeta;
}

/**
 * A preview or install in flight. `phase` moves through archive → files → upload
 * → done, and `done`/`total` are the counters for that phase.
 */
export interface PackJob {
	kind: 'preview' | 'install';
	phase: string;
	source: string;
	profile: string;
	done: number;
	total: number;
	error?: string;
	startedAt: string;
	endedAt?: string;
	result?: PackJobResult;
}

/** One published version of a pack, as the picker shows it. */
export interface PackVersion {
	id: string;
	number: string;
	name: string;
	type: 'release' | 'beta' | 'alpha';
	gameVersions: string[];
	loaders: string[];
	publishedAt: string;
	size: number;
	/** This server can run it: a Fabric build for the Minecraft version we boot. */
	bootable: boolean;
	/** The version the profile already holds. */
	installed: boolean;
	reason?: string;
}

export interface PackVersions {
	slug: string;
	name: string;
	profile: string;
	installed?: string;
	choices: PackVersion[];
	total: number;
	/** Versions the list leaves out (all runnable ones are always included). */
	skipped: number;
	/** Newest runnable version that is not the installed one. */
	newer?: string;
}

export interface PackProfile {
	id: string;
	active: boolean;
	pack?: PackMeta;
	mods: number;
	bytes: number;
	hasWorld: boolean;
	/** How big the saved world is, so the page can say what it is deleting. */
	worldBytes?: number;
	worldPack?: PackMeta;
}

export interface PackOverview {
	active: string;
	profiles: PackProfile[];
	job?: PackJob;
	editable: boolean;
	state: ServerState;
	/** The Minecraft version this image can boot; the vanilla button names it. */
	supportedMc: string;
	detail?: string;
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

export const act = (action: ActionKind, token: string, type = '') =>
	post<{ message?: string }>('/api/action', { action, token, type });

export const sendConsole = (command: string, token: string) =>
	post<{ output: string; truncated: boolean }>('/api/console', { command, token });

export const playerAction = (action: PlayerAction, name: string, token: string) =>
	post<{ name: string; message: string; applied: boolean }>('/api/players', {
		action,
		name,
		token
	});

export const getPack = async (): Promise<PackOverview> =>
	json<PackOverview>(await fetch('/api/pack', { headers: { accept: 'application/json' } }));

/** Moves the profile's world to trash/ — recoverable for two weeks. */
export const packWorldDelete = (token: string, profile: string) =>
	post<{ message: string }>('/api/pack/world/delete', { token, profile });

/** Removes the profile: its world (through the trash), backups and files. */
export const packDelete = (token: string, profile: string) =>
	post<{ message: string }>('/api/pack/delete', { token, profile });

/** profile scopes the list to a row: a profile named --profile potato is not
 *  named after its pack, so deriving it from the source would answer about a
 *  different profile. Empty derives. */
export const packVersions = (source: string, token: string, profile = '') =>
	post<PackVersions>('/api/pack/versions', { source, token, profile });

/** A server with no mods: Fabric and nothing loaded. No project, no preview. */
export const packVanilla = (token: string, profile = '', newWorld = false) =>
	post<PackJob>('/api/pack/vanilla', { token, profile, activate: true, newWorld });

export const packPreview = (
	source: string,
	token: string,
	memoryGB: number,
	version = '',
	profile = ''
) => post<PackJob>('/api/pack/preview', { source, version, token, memoryGB, profile });

export const packInstall = (
	req: {
		source: string;
		version?: string;
		profile: string;
		memoryGB: number;
		newWorld: boolean;
		keepWorld: boolean;
		activate: boolean;
	},
	token: string
) => post<PackJob>('/api/pack/install', { ...req, token });

export const packUse = (profile: string, token: string) =>
	post<{ active: string }>('/api/pack/use', { profile, token });

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

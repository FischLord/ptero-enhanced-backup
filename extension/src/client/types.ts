// Antworten der Extension-API (siehe routes/admin.php); die Gateway-Felder folgen docs/admin-api.md.

export interface PanelServerRef {
    id: number;
    name: string;
    node: string | null;
    owner: string | null;
    /** Backup-Limit des Servers im Panel und Anzahl laufender plus erfolgreicher Backups (wie das Panel zaehlt). */
    backup_limit: number;
    backup_count: number;
}

/** Jobs der Offsite-Kopie; sie erscheinen in der Wartungsliste, lassen sich dort aber nicht anstossen. */
export type OffsiteJobName = 'copy' | 'forget-secondary' | 'prune-secondary' | 'check-secondary';

export interface MaintenanceJob {
    id: number;
    type: 'prune' | 'check' | 'stats' | 'trash-purge' | OffsiteJobName;
    trigger: 'schedule' | 'manual';
    started_at: string | null;
    finished_at: string | null;
    result: 'queued' | 'running' | 'ok' | 'failed' | 'skipped';
    message: string;
    bytes_freed: number | null;
}

export interface GatewayStatus {
    version: string;
    uptime_seconds: number;
    repo: { type: string; location: string; reachable: boolean; checked_at: string | null };
    ingest: { running: number; queued: number };
    dumps: { active: number };
    spool: { used_bytes: number; limit_bytes: number };
    maintenance: {
        prune_enabled: boolean;
        next_window: string | null;
        last_prune: MaintenanceJob | null;
        last_check: MaintenanceJob | null;
        running_job: MaintenanceJob | null;
        /** Prune-Laeufe in Folge, die als "skipped: busy" uebersprungen wurden (Gateway ab 0.2.0). */
        prune_skipped_consecutive?: number;
        /** Kurze Warntexte des Gateways (englisch), leer = keine. */
        warnings?: string[];
    };
}

export interface GatewaySummary {
    backups_count: number;
    logical_bytes: number;
    received_bytes: number;
    data_added_total: number;
    upload_saved_bytes: number;
    stored_bytes: number | null;
    referenced_bytes: number | null;
    stored_bytes_exact: boolean | null;
    stored_at: string | null;
    saved_bytes: number | null;
    saved_percent: number | null;
    factor: number | null;
    success_rate_30d: number | null;
    failures_30d: number;
    last_backup_at: string | null;
    target: StorageTarget | null;
    /** Fehlt bei Gateways vor 0.2.0. */
    secondary?: SecondaryStatus;
    /** Papierkorb (Gateway ab 0.2.0); `logical_bytes` = Originalgroesse der enthaltenen Backups. */
    trash?: { count: number; logical_bytes: number; next_purge_at: string | null };
}

/** Offsite-Kopie (Zweitziel); `repo` ist ohne Zugangsdaten. */
export interface SecondaryStatus {
    configured: boolean;
    repo: string | null;
    type: string | null;
    state: 'off' | 'ok' | 'copying' | 'pending' | 'error';
    running: boolean;
    last_copy_at: string | null;
    last_copy_result: 'ok' | 'failed' | null;
    last_error: string;
    /** Der naechtliche Forget-Schritt ist durch eine Sicherung gestoppt, bis er freigegeben wird. */
    forget_blocked?: boolean;
    lag_snapshots: number | null;
    lag_since: string | null;
    stored_bytes: number | null;
    stored_at: string | null;
    next_copy_at: string | null;
}

/** Platz auf dem Backup-Ziel; `exact` false = Schaetzung aus PEB_REPO_CAPACITY_BYTES. */
export interface StorageTarget {
    total_bytes: number;
    free_bytes: number;
    used_bytes: number;
    peb_bytes: number;
    other_bytes: number;
    exact: boolean;
    days_until_full: number | null;
    avg_data_added: number | null;
    backups_remaining: number | null;
}

export interface HistoryDay {
    date: string;
    backups_ok: number;
    backups_failed: number;
    logical_bytes: number;
    data_added: number;
    stored_bytes: number | null;
}

export interface Forecast {
    capacity_bytes: number;
    free_bytes?: number;
    used_bytes: number;
    growth_bytes_per_day: number | null;
    days_until_full: number | null;
    full_at: string | null;
}

export interface History {
    days: number;
    series: HistoryDay[];
    forecast?: Forecast;
}

export interface Savings {
    currency: string;
    price_per_tb: number;
    saved_tb: number | null;
    saved_per_month: number | null;
    without_per_month: number | null;
    stored_per_month: number | null;
}

export interface Overview {
    status: GatewayStatus;
    summary: GatewaySummary;
    history: History;
    savings: Savings;
    settings: { currency: string; price_per_tb: number; warn_hours: number };
    generated_at: string;
}

export type ServerHealth = 'ok' | 'warning' | 'failed' | 'never';

export interface ServerRow {
    server_uuid: string;
    server: PanelServerRef | null;
    backup_count: number;
    last_success_at: string | null;
    last_status: 'complete' | 'failed' | null;
    last_error: string;
    last_finished_at: string | null;
    logical_bytes: number;
    data_added_total: number;
    avg_duration_ms: number;
    last_duration_ms: number;
    last_backup_bytes: number;
    last_data_added: number;
    /** Space of this server's snapshots on the target (compressed, deduplicated); null until measured or on older gateways. */
    stored_bytes?: number | null;
    stored_at?: string | null;
    health: ServerHealth;
}

export interface ServersResponse {
    items: ServerRow[];
    warn_hours: number;
    generated_at: string;
}

export type BackupStatus = 'pending' | 'complete' | 'failed' | 'deleted';

export interface BackupRow {
    backup_uuid: string;
    server_uuid: string;
    server: PanelServerRef | null;
    status: BackupStatus;
    error: string;
    snapshot_id: string;
    received_bytes: number;
    logical_bytes: number;
    data_added: number;
    total_bytes_processed: number;
    file_count: number;
    index_truncated: boolean;
    started_at: string | null;
    finished_at: string | null;
    duration_ms: number;
}

export interface Paged<T> {
    items: T[];
    total: number;
    limit: number;
    offset: number;
}

export interface FileEntry {
    name: string;
    path: string;
    type: 'dir' | 'file' | 'symlink' | 'other';
    size: number;
    mode: string | null;
    mtime: string | null;
}

export interface FilesResponse extends Paged<FileEntry> {
    backup_uuid: string;
    path: string;
    index_truncated: boolean;
}

export type MaintenanceJobName = 'prune' | 'check' | 'stats';
/** Was `POST maintenance/run` annimmt: die Wartungsjobs und die Offsite-Kopie. */
export type RunJobName = MaintenanceJobName | 'copy';

export interface ApiErrorInfo {
    code: string;
    message: string;
    status: number | null;
}

/** Antwort von GET /edition: ob die Pro-Extension aktiv ist (siehe PHP: Pro\ProRegistry). */
export interface Edition {
    pro: { installed: boolean; id: string | null; version: string | null; features: string[] };
}

export type CheckStatus = 'ok' | 'warn' | 'fail';

export interface SetupCheck {
    id: string;
    status: CheckStatus;
    hint: string | null;
    value?: number | null;
}

export interface SetupState {
    available: { egg_import: boolean; egg_update: boolean; server_create: boolean; s3_probe: boolean };
    ready: boolean;
    checks: SetupCheck[];
    egg: { name: string | null; exists: boolean; id: number | null; error: string | null };
    defaults: { name: string; bucket: string; region: string; repository: string; memory: number; disk: number };
    snippets: { env: string; wings: string };
}

export interface SetupNode {
    id: number;
    name: string;
    fqdn: string;
    free_allocations: number;
}

export interface SetupAllocation {
    id: number;
    ip: string;
    alias: string | null;
    port: number;
}

export interface SetupUser {
    id: number;
    username: string;
    email: string;
}

export interface GatewayServerForm {
    name: string;
    owner_id: number;
    allocation_id: number;
    memory: number;
    disk: number;
    bucket: string;
    region: string;
    repository: string;
    host: string;
}

/** Antwort von POST /setup/server. Enthaelt die Geheimnisse; nur im Speicher halten, nie cachen oder ablegen. */
export interface CreatedGatewayServer {
    server: { id: number; uuid: string; name: string };
    gateway_url: string;
    credentials: { access_key: string; secret_key: string; admin_token: string; restic_password: string; bucket: string; region: string };
    snippets: { env: string; wings: string };
}

export interface SelfTestResult {
    ok: boolean;
    checks: SetupCheck[];
}

/** Gateway-weite Bestaetigung, dass das Restic-Passwort an einem zweiten Ort liegt (Gateway ab 0.2.0). */
export interface PasswordAck {
    acknowledged: boolean;
    acknowledged_at: string | null;
    by: string;
}

/** Ein restic-Schluessel des Repositorys. `emergency` = ueber die Extension erzeugt. */
export interface RepoKey {
    id: string;
    short_id: string;
    created_at: string | null;
    host: string;
    user: string;
    current: boolean;
    emergency: boolean;
    label: string;
}

/** Antwort von POST /keys. `password` kommt genau einmal vor; nur im Speicher halten, nie cachen oder ablegen. */
export interface CreatedKey {
    id: string;
    short_id: string;
    label: string;
    host: string;
    user: string;
    created_at: string | null;
    password: string;
}

/** Daten fuer das Notfallblatt (ohne Passwort). */
export interface EmergencySheetData {
    gateway_url: string;
    repo: { type: string; location: string };
    secondary: { type: string; repo: string } | null;
    servers: { uuid: string; name: string | null }[];
    generated_at: string;
}

/** Warum ein Backup im Papierkorb liegt. */
export type TrashReason = 'panel_delete' | 'server_deleted';

export interface TrashRow {
    backup_uuid: string;
    server_uuid: string;
    server: PanelServerRef | null;
    reason: TrashReason;
    logical_bytes: number;
    received_bytes: number;
    finished_at: string | null;
    trashed_at: string | null;
    purge_after: string | null;
}

export interface TrashResponse {
    total: number;
    logical_bytes: number;
    items: TrashRow[];
}

/** Ein konfigurierter Benachrichtigungskanal; `target` ist vom Gateway bereits geschwaerzt (Gateway ab 0.3). */
export interface NotifyChannel {
    type: 'ntfy' | 'webhook' | 'smtp';
    target: string;
    format?: string;
}

export interface NotifyConfig {
    lang: string;
    channels: NotifyChannel[];
}

export interface NotifyTestResult {
    type: NotifyChannel['type'];
    target: string;
    ok: boolean;
    error?: string;
}

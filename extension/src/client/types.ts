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

export interface MaintenanceJob {
    id: number;
    type: 'prune' | 'check' | 'stats';
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

export interface ApiErrorInfo {
    code: string;
    message: string;
    status: number | null;
}

/** Antwort von GET /edition: ob die Pro-Extension aktiv ist (siehe PHP: Pro\ProRegistry). */
export interface Edition {
    pro: { installed: boolean; id: string | null; version: string | null; features: string[] };
}

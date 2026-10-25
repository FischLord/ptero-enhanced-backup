import type { BackupRow, FilesResponse, MaintenanceJob, Overview, ServersResponse } from '../../src/client/types';

export const SERVER_A = '2b7fdd72-690d-4ffa-8c1e-5c299ab53da5';
export const SERVER_B = '457727f5-332e-43b8-af62-37e7baecbcf9';
export const BACKUP_A = 'dc936ab6-2ac7-497a-8563-9626376dd2cb';

export const job = (over: Partial<MaintenanceJob> = {}): MaintenanceJob => ({
    id: 1,
    type: 'prune',
    trigger: 'manual',
    started_at: '2026-10-24T14:47:36Z',
    finished_at: '2026-10-24T14:47:37Z',
    result: 'ok',
    message: 'prune finished, 864017186 bytes freed',
    bytes_freed: 864017186,
    ...over,
});

export const overview = (over: Partial<Overview> = {}): Overview => ({
    status: {
        version: 'dev',
        uptime_seconds: 861,
        repo: { type: 'local', location: '/mnt/peb/repo', reachable: true, checked_at: '2026-10-24T15:01:12Z' },
        ingest: { running: 1, queued: 2 },
        dumps: { active: 0 },
        spool: { used_bytes: 0, limit_bytes: 8589934592 },
        maintenance: {
            prune_enabled: true,
            next_window: '2026-10-25T01:00:00Z',
            last_prune: job(),
            last_check: job({ id: 2, type: 'check', message: 'no errors found', bytes_freed: null }),
            running_job: null,
        },
    },
    summary: {
        backups_count: 3,
        logical_bytes: 9969518592,
        received_bytes: 9970374352,
        data_added_total: 5120161845,
        upload_saved_bytes: 4849356747,
        stored_bytes: 1759143769,
        referenced_bytes: 1758778885,
        stored_bytes_exact: true,
        stored_at: '2026-10-24T14:47:49Z',
        saved_bytes: 8210374823,
        saved_percent: 82.35,
        factor: 5.67,
        success_rate_30d: 1,
        failures_30d: 0,
        last_backup_at: '2026-10-24T14:09:33Z',
        target: {
            total_bytes: 4000000000000,
            free_bytes: 3100000000000,
            used_bytes: 900000000000,
            peb_bytes: 1759143769,
            other_bytes: 898240856231,
            exact: true,
            days_until_full: 4083,
            avg_data_added: 1706720615,
            backups_remaining: 1816,
        },
    },
    history: {
        days: 3,
        series: [
            { date: '2026-10-22', backups_ok: 0, backups_failed: 0, logical_bytes: 0, data_added: 0, stored_bytes: null },
            { date: '2026-10-23', backups_ok: 1, backups_failed: 1, logical_bytes: 5000000000, data_added: 100000000, stored_bytes: 1000000000 },
            { date: '2026-10-24', backups_ok: 3, backups_failed: 0, logical_bytes: 9969518592, data_added: 5120161845, stored_bytes: 1759143769 },
        ],
        forecast: { capacity_bytes: 1099511627776, used_bytes: 1759143769, growth_bytes_per_day: 759143769, days_until_full: 1446.5, full_at: '2030-10-01T00:00:00Z' },
    },
    savings: { currency: 'EUR', price_per_tb: 6, saved_tb: 0.0082, saved_per_month: 0.05, without_per_month: 0.06, stored_per_month: 0.01 },
    settings: { currency: 'EUR', price_per_tb: 6, warn_hours: 26 },
    generated_at: '2026-10-24T15:00:00Z',
    ...over,
});

export const servers = (): ServersResponse => ({
    warn_hours: 26,
    generated_at: '2026-10-24T15:00:00Z',
    items: [
        {
            server_uuid: SERVER_A,
            server: { id: 4, name: 'Satisfactory 2024', node: 'node1.example.com', owner: 'admin', backup_limit: 3, backup_count: 2 },
            backup_count: 2,
            last_success_at: '2026-10-24T14:09:33Z',
            last_status: 'complete',
            last_error: '',
            last_finished_at: '2026-10-24T14:09:33Z',
            logical_bytes: 9699718144,
            data_added_total: 4850344438,
            avg_duration_ms: 21485,
            last_duration_ms: 13547,
            last_backup_bytes: 4849859072,
            last_data_added: 120586240,
            stored_bytes: 2147483648,
            stored_at: '2026-10-24T14:10:00Z',
            health: 'ok',
        },
        {
            server_uuid: SERVER_B,
            server: null,
            backup_count: 0,
            last_success_at: null,
            last_status: 'failed',
            last_error: 'timeout',
            last_finished_at: '2026-10-24T09:00:00Z',
            logical_bytes: 0,
            data_added_total: 0,
            avg_duration_ms: 0,
            last_duration_ms: 21600000,
            last_backup_bytes: 0,
            last_data_added: 0,
            stored_bytes: null,
            stored_at: null,
            health: 'failed',
        },
    ],
});

export const backup = (over: Partial<BackupRow> = {}): BackupRow => ({
    backup_uuid: BACKUP_A,
    server_uuid: SERVER_A,
    server: null,
    status: 'complete',
    error: '',
    snapshot_id: 'abc',
    received_bytes: 4850275362,
    logical_bytes: 4849859072,
    data_added: 242683,
    total_bytes_processed: 4849859072,
    file_count: 1265,
    index_truncated: false,
    started_at: '2026-10-24T14:09:19Z',
    finished_at: '2026-10-24T14:09:33Z',
    duration_ms: 13547,
    ...over,
});

export const files = (): FilesResponse => ({
    backup_uuid: BACKUP_A,
    path: '',
    index_truncated: false,
    total: 2,
    limit: 100,
    offset: 0,
    items: [
        { name: 'world', path: 'world', type: 'dir', size: 0, mode: '0755', mtime: '2026-10-23T20:11:02Z' },
        { name: 'server.properties', path: 'server.properties', type: 'file', size: 1234, mode: '0644', mtime: '2026-10-24T02:59:58Z' },
    ],
});

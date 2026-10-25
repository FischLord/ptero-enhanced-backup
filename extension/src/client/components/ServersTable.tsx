import { useMemo, useState } from 'react';
import { Alert, Button, Input, Table, type TableColumn } from '@pterodactyl/sdk';
import { formatBytes, formatDateTime, formatDuration, formatNumber, formatRelative } from '../format';
import { useT } from '../i18n';
import type { ServerHealth, ServerRow } from '../types';
import { HealthBadge, Hint, Muted, ServerLabel } from './Common';

const ORDER: Record<ServerHealth, number> = { failed: 0, warning: 1, never: 2, ok: 3 };

export const serverName = (row: ServerRow, unknown: string): string => row.server?.name ?? `${unknown} ${row.server_uuid}`;

export default function ServersTable({ rows, warnHours, onOpen }: { rows: ServerRow[]; warnHours: number; onOpen: (row: ServerRow) => void }) {
    const { t, locale } = useT();
    const [filter, setFilter] = useState('');
    const unknown = t('unknown_server');

    const visible = useMemo(() => {
        const needle = filter.trim().toLowerCase();
        const matches = needle
            ? rows.filter((r) => `${serverName(r, unknown)} ${r.server?.owner ?? ''} ${r.server?.node ?? ''} ${r.server_uuid}`.toLowerCase().includes(needle))
            : rows;

        return [...matches].sort((a, b) => ORDER[a.health] - ORDER[b.health] || serverName(a, unknown).localeCompare(serverName(b, unknown)));
    }, [rows, filter, unknown]);

    const attention = rows.filter((r) => r.health !== 'ok').length;

    const columns: TableColumn<ServerRow>[] = [
        {
            id: 'server',
            label: t('col_server'),
            sortValue: (r) => serverName(r, unknown).toLowerCase(),
            cell: (r) => <ServerLabel server={r.server} uuid={r.server_uuid} />,
        },
        {
            id: 'last',
            label: t('col_last_success'),
            sortValue: (r) => r.last_success_at,
            cell: (r) => (
                <div className={'peb:flex peb:flex-col peb:items-start peb:gap-1'}>
                    {r.last_success_at ? <span title={formatDateTime(r.last_success_at, locale)}>{formatRelative(r.last_success_at, locale)}</span> : <Muted>{t('never')}</Muted>}
                    <HealthBadge health={r.health} warnHours={warnHours} />
                    {r.last_status === 'failed' && r.last_error && <Muted>{r.last_error}</Muted>}
                </div>
            ),
        },
        {
            id: 'size',
            label: t('col_server_size'),
            sortValue: (r) => r.last_backup_bytes,
            cellClassName: 'peb:whitespace-nowrap',
            cell: (r) => <Hint text={t('tip_col_size_server')}>{formatBytes(r.last_backup_bytes, locale)}</Hint>,
        },
        {
            id: 'added',
            label: t('col_data_added'),
            sortValue: (r) => r.last_data_added,
            cellClassName: 'peb:whitespace-nowrap',
            cell: (r) => <Hint text={t('tip_col_added_server')}>{formatBytes(r.last_data_added, locale)}</Hint>,
        },
        {
            id: 'stored',
            label: t('col_stored'),
            sortValue: (r) => r.stored_bytes ?? -1,
            cellClassName: 'peb:whitespace-nowrap',
            cell: (r) =>
                r.stored_bytes === null || r.stored_bytes === undefined ? (
                    <Muted>–</Muted>
                ) : (
                    <Hint text={t('tip_col_stored_server')}>{formatBytes(r.stored_bytes, locale)}</Hint>
                ),
        },
        { id: 'count', label: t('col_backups'), sortValue: (r) => r.backup_count, cell: (r) => formatNumber(r.backup_count, locale) },
        {
            id: 'limit',
            label: t('col_limit'),
            sortValue: (r) => (r.server ? r.server.backup_count / Math.max(r.server.backup_limit, 1) : -1),
            cellClassName: 'peb:whitespace-nowrap',
            cell: (r) => {
                if (!r.server) return <Muted>-</Muted>;
                if (r.server.backup_limit === 0) return <Muted>{t('limit_none')}</Muted>;
                const text = t('limit_value', { count: r.server.backup_count, limit: r.server.backup_limit });

                return r.server.backup_count >= r.server.backup_limit ? <Hint text={t('limit_full')}>{text}</Hint> : text;
            },
        },
        { id: 'duration', label: t('col_duration'), sortValue: (r) => r.last_duration_ms, cell: (r) => formatDuration(r.last_duration_ms) },
        {
            id: 'actions',
            label: '',
            cell: (r) => (
                <Button size={'xsmall'} isSecondary onClick={() => onOpen(r)}>
                    {t('show_backups')}
                </Button>
            ),
        },
    ];

    return (
        <div className={'peb:flex peb:flex-col peb:gap-3'}>
            {attention > 0 && (
                <Alert type={'warning'}>{t('servers_attention', { count: attention, hours: warnHours })}</Alert>
            )}
            <div className={'peb:max-w-sm'}>
                <Input value={filter} onChange={(e) => setFilter(e.target.value)} placeholder={t('filter_servers')} aria-label={t('filter_servers')} />
            </div>
            <Table
                rows={visible}
                columns={columns}
                keyOf={(r) => r.server_uuid}
                emptyState={rows.length === 0 ? t('servers_empty') : t('servers_no_match')}
            />
            <p className={'peb:text-xs peb:text-muted-foreground'}>{t('cols_help')}</p>
        </div>
    );
}

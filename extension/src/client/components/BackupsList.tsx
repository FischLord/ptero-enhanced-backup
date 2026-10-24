import { useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { Badge, Button, Table, type TableColumn } from '@pterodactyl/sdk';
import { BACKUPS_PER_PAGE, getBackups, keys } from '../api';
import { formatBytes, formatDateTime, formatDuration, formatNumber } from '../format';
import { useT } from '../i18n';
import type { BackupRow, BackupStatus } from '../types';
import { Loaded } from './Loaded';
import { Hint, Muted } from './Common';

const TONE: Record<BackupStatus, 'success' | 'danger' | 'info' | 'neutral'> = { complete: 'success', failed: 'danger', pending: 'info', deleted: 'neutral' };

/** Backups eines Servers (serverUuid) oder aller Server (null), seitenweise. */
export default function BackupsList({ serverUuid, onBrowse }: { serverUuid: string | null; onBrowse: (backup: BackupRow) => void }) {
    const { t, locale } = useT();
    const [page, setPage] = useState(1);
    const query = useQuery({ queryKey: keys.backups(serverUuid, page), queryFn: () => getBackups(serverUuid, page), placeholderData: (previous) => previous, retry: false });

    return (
        <Loaded query={query}>
            {(data) => {
                const columns: TableColumn<BackupRow>[] = [
                    {
                        id: 'started',
                        label: t('col_started'),
                        cell: (r) => formatDateTime(r.started_at, locale),
                    },
                    {
                        id: 'status',
                        label: t('col_status'),
                        cell: (r) => (
                            <div className={'peb:flex peb:flex-col peb:items-start peb:gap-1'}>
                                <Badge tone={TONE[r.status]} variant={'soft'}>
                                    {t(`backup_${r.status}`)}
                                </Badge>
                                {r.status === 'failed' && r.error && <Muted>{r.error}</Muted>}
                                {r.index_truncated && <Muted>{t('index_truncated_short')}</Muted>}
                            </div>
                        ),
                    },
                    { id: 'logical', label: <Hint text={t('tip_col_logical_backup')}>{t('col_logical')}</Hint>, cell: (r) => formatBytes(r.logical_bytes, locale) },
                    { id: 'added', label: <Hint text={t('tip_col_added_backup')}>{t('col_added')}</Hint>, cell: (r) => formatBytes(r.data_added, locale) },
                    { id: 'files', label: t('col_files'), cell: (r) => formatNumber(r.file_count, locale) },
                    { id: 'duration', label: t('col_duration'), cell: (r) => formatDuration(r.duration_ms) },
                    {
                        id: 'actions',
                        label: '',
                        cell: (r) => (
                            <Button size={'xsmall'} isSecondary disabled={r.status !== 'complete'} onClick={() => onBrowse(r)}>
                                {t('browse_files')}
                            </Button>
                        ),
                    },
                ];

                return (
                    <Table
                        rows={data.items}
                        columns={columns}
                        keyOf={(r) => r.backup_uuid}
                        isFetching={query.isFetching}
                        emptyState={t('backups_empty')}
                        pagination={{
                            page,
                            perPage: BACKUPS_PER_PAGE,
                            total: data.total,
                            count: data.items.length,
                            itemLabel: t('backups_label'),
                            onPageSelect: setPage,
                        }}
                    />
                );
            }}
        </Loaded>
    );
}

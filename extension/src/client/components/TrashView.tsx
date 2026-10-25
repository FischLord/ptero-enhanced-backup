import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Badge, Button, Dialog, Table, toast, type TableColumn } from '@pterodactyl/sdk';
import { EXTENSION_ID, errorInfo, getTrash, keys, purgeFromTrash, restoreFromTrash } from '../api';
import { formatBytes, formatDateTime, formatRelative } from '../format';
import { useT } from '../i18n';
import type { TrashRow } from '../types';
import { Muted, ServerLabel } from './Common';
import { Loaded } from './Loaded';

/** Papierkorb: im Panel geloeschte Backups und Backups geloeschter Server, mit Wiederherstellen und endgueltigem Loeschen. */
export default function TrashView() {
    const { t, locale } = useT();
    const queryClient = useQueryClient();
    const [purging, setPurging] = useState<TrashRow | null>(null);
    const query = useQuery({ queryKey: keys.trash(), queryFn: getTrash, retry: false, refetchInterval: 60000 });

    const refresh = () => void queryClient.invalidateQueries({ queryKey: [EXTENSION_ID] });
    const onError = (error: unknown) => toast.error(errorInfo(error).message || t('error_unknown'));

    const restore = useMutation({
        mutationFn: (row: TrashRow) => restoreFromTrash(row.backup_uuid),
        onSuccess: () => {
            toast.success(t('trash_restored'));
            refresh();
        },
        onError,
    });
    const purge = useMutation({
        mutationFn: (row: TrashRow) => purgeFromTrash(row.backup_uuid),
        onSuccess: () => {
            toast.success(t('trash_purged'));
            setPurging(null);
            refresh();
        },
        onError: (error) => {
            onError(error);
            setPurging(null);
        },
    });

    const columns: TableColumn<TrashRow>[] = [
        {
            id: 'server',
            label: t('col_server'),
            cell: (r) => <ServerLabel server={r.server} uuid={r.server_uuid} />,
        },
        {
            id: 'backup',
            label: t('col_backup'),
            cell: (r) => (
                <div className={'peb:flex peb:flex-col peb:items-start peb:gap-1'}>
                    <span className={'peb:font-mono peb:text-xs'}>{r.backup_uuid}</span>
                    <Badge tone={r.reason === 'server_deleted' ? 'warning' : 'neutral'} variant={'soft'}>
                        {t(r.reason === 'server_deleted' ? 'trash_reason_server_deleted' : 'trash_reason_panel_delete')}
                    </Badge>
                </div>
            ),
        },
        {
            id: 'size',
            label: t('col_size'),
            sortValue: (r) => r.logical_bytes,
            cellClassName: 'peb:whitespace-nowrap',
            cell: (r) => formatBytes(r.logical_bytes, locale),
        },
        {
            id: 'trashed',
            label: t('col_trashed'),
            sortValue: (r) => r.trashed_at,
            cell: (r) => <span title={formatDateTime(r.trashed_at, locale)}>{formatRelative(r.trashed_at, locale)}</span>,
        },
        {
            id: 'purge',
            label: t('col_purge'),
            sortValue: (r) => r.purge_after,
            cell: (r) => (
                <div className={'peb:flex peb:flex-col'}>
                    <span>{formatRelative(r.purge_after, locale)}</span>
                    <Muted>{formatDateTime(r.purge_after, locale)}</Muted>
                </div>
            ),
        },
        {
            id: 'actions',
            label: '',
            cell: (r) => (
                <div className={'peb:flex peb:flex-wrap peb:gap-2'}>
                    <Button size={'xsmall'} isSecondary disabled={restore.isPending} onClick={() => restore.mutate(r)}>
                        {t('trash_restore')}
                    </Button>
                    <Button size={'xsmall'} isSecondary onClick={() => setPurging(r)}>
                        {t('trash_purge')}
                    </Button>
                </div>
            ),
        },
    ];

    return (
        <div className={'peb:flex peb:flex-col peb:gap-3'}>
            <p className={'peb:max-w-3xl peb:text-sm peb:text-muted-foreground'}>{t('trash_intro')}</p>
            <Loaded query={query}>
                {(data) => (
                    <>
                        {data.items.length > 0 && <Muted>{t('trash_summary', { count: data.total, size: formatBytes(data.logical_bytes, locale) })}</Muted>}
                        <Table rows={data.items} columns={columns} keyOf={(r) => r.backup_uuid} isFetching={query.isFetching} emptyState={t('trash_empty')} />
                    </>
                )}
            </Loaded>
            <Dialog.Confirm
                open={purging !== null}
                onClose={() => setPurging(null)}
                title={t('trash_purge_title')}
                confirm={t('trash_purge_confirm')}
                pending={purge.isPending}
                onConfirmed={() => {
                    if (purging) purge.mutate(purging);
                }}
            >
                <p>{t('trash_purge_text')}</p>
            </Dialog.Confirm>
        </div>
    );
}

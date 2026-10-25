import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Alert, Button, Checkbox, Dialog, Table, toast, type TableColumn } from '@pterodactyl/sdk';
import { EXTENSION_ID, errorInfo, getMaintenance, keys, runMaintenance } from '../api';
import { formatBytes, formatDateTime, formatDuration } from '../format';
import { jobKey, useT, type MessageKey } from '../i18n';
import type { MaintenanceJob, MaintenanceJobName } from '../types';
import { JobResultBadge, Muted } from './Common';
import { Loaded } from './Loaded';
import ProHint from './ProHint';

const JOBS: MaintenanceJobName[] = ['prune', 'check', 'stats'];
const active = (jobs: MaintenanceJob[] | undefined) => !!jobs?.some((j) => j.result === 'queued' || j.result === 'running');

function jobDuration(job: MaintenanceJob): number | null {
    if (!job.started_at || !job.finished_at) return null;

    return new Date(job.finished_at).getTime() - new Date(job.started_at).getTime();
}

export default function MaintenanceView() {
    const { t, locale } = useT();
    const queryClient = useQueryClient();
    const [open, setOpen] = useState(false);
    const [selected, setSelected] = useState<Record<MaintenanceJobName, boolean>>({ prune: true, check: true, stats: true });
    const query = useQuery({
        queryKey: keys.maintenance(),
        queryFn: () => getMaintenance(50),
        retry: false,
        refetchInterval: (q) => (active(q.state.data?.items) ? 4000 : 30000),
    });

    const chosen = JOBS.filter((j) => selected[j]);
    const run = useMutation({
        mutationFn: () => runMaintenance(chosen),
        onSuccess: () => {
            toast.success(t('maintenance_started'));
            setOpen(false);
            void queryClient.invalidateQueries({ queryKey: [EXTENSION_ID] });
        },
        onError: (error) => {
            const info = errorInfo(error);
            toast.error(info.code === 'busy' ? t('maintenance_busy') : info.message || t('error_unknown'));
            setOpen(false);
        },
    });

    const columns: TableColumn<MaintenanceJob>[] = [
        { id: 'started', label: t('col_started'), cell: (j) => formatDateTime(j.started_at, locale) },
        { id: 'type', label: t('col_job'), cell: (j) => t(jobKey(j.type)) },
        { id: 'trigger', label: t('col_trigger'), cell: (j) => t(`trigger_${j.trigger}`) },
        { id: 'result', label: t('col_result'), cell: (j) => <JobResultBadge result={j.result} /> },
        { id: 'duration', label: t('col_duration'), cell: (j) => formatDuration(jobDuration(j)) },
        {
            id: 'message',
            label: t('col_message'),
            cell: (j) => (
                <span className={'peb:flex peb:flex-col'}>
                    <span>{j.message}</span>
                    {j.bytes_freed !== null && <Muted>{t('freed', { size: formatBytes(j.bytes_freed, locale) })}</Muted>}
                </span>
            ),
        },
    ];

    return (
        <div className={'peb:flex peb:flex-col peb:gap-3'}>
            <div className={'peb:flex peb:flex-wrap peb:items-center peb:justify-between peb:gap-3'}>
                <p className={'peb:max-w-2xl peb:text-sm peb:text-muted-foreground'}>{t('maintenance_intro')}</p>
                <Button color={'primary'} onClick={() => setOpen(true)} disabled={active(query.data?.items)}>
                    {t('run_now')}
                </Button>
            </div>
            <ProHint text={'pro_retention_hint'} />
            <Loaded query={query}>
                {(data) => (
                    <>
                        {active(data.items) && <Alert type={'info'}>{t('maintenance_running')}</Alert>}
                        <Table rows={data.items} columns={columns} keyOf={(j) => String(j.id)} isFetching={query.isFetching} emptyState={t('maintenance_empty')} />
                    </>
                )}
            </Loaded>
            <Dialog.Confirm
                open={open}
                onClose={() => setOpen(false)}
                title={t('run_confirm_title')}
                confirm={t('run_confirm_button')}
                pending={run.isPending}
                onConfirmed={() => {
                    if (chosen.length > 0) run.mutate();
                }}
            >
                <div className={'peb:flex peb:flex-col peb:gap-3'}>
                    <p>{t('run_confirm_text')}</p>
                    <ul className={'peb:flex peb:flex-col peb:gap-2'}>
                        {JOBS.map((job) => (
                            <li key={job}>
                                <label className={'peb:flex peb:cursor-pointer peb:items-start peb:gap-2'}>
                                    <Checkbox checked={selected[job]} onChange={(checked) => setSelected((s) => ({ ...s, [job]: checked }))} />
                                    <span className={'peb:flex peb:flex-col'}>
                                        <span className={'peb:font-medium'}>{t(`job_${job}` as MessageKey)}</span>
                                        <span className={'peb:text-xs peb:text-muted-foreground'}>{t(`job_${job}_hint` as MessageKey)}</span>
                                    </span>
                                </label>
                            </li>
                        ))}
                    </ul>
                    {chosen.length === 0 && <Alert type={'warning'}>{t('run_choose_one')}</Alert>}
                </div>
            </Dialog.Confirm>
        </div>
    );
}

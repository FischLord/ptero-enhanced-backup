import type { ReactNode } from 'react';
import { Badge, Card, ProgressBar } from '@pterodactyl/sdk';
import { formatBytes, formatDateTime, formatNumber, formatPercent, formatRelative } from '../format';
import { useT } from '../i18n';
import type { MaintenanceJob, Overview } from '../types';
import { JobResultBadge, Muted } from './Common';

function Row({ label, children }: { label: string; children: ReactNode }) {
    return (
        <div className={'peb:flex peb:flex-col peb:gap-0.5 peb:py-2 peb:sm:flex-row peb:sm:items-start peb:sm:gap-4'}>
            <dt className={'peb:text-sm peb:text-muted-foreground peb:sm:w-48 peb:sm:shrink-0'}>{label}</dt>
            <dd className={'peb:flex peb:min-w-0 peb:flex-1 peb:flex-col peb:gap-1 peb:text-sm peb:text-foreground'}>{children}</dd>
        </div>
    );
}

function JobRow({ job }: { job: MaintenanceJob | null }) {
    const { t, locale } = useT();
    if (!job) return <Muted>{t('never_ran')}</Muted>;

    return (
        <>
            <span className={'peb:flex peb:flex-wrap peb:items-center peb:gap-2'}>
                <JobResultBadge result={job.result} />
                <span title={formatDateTime(job.finished_at ?? job.started_at, locale)}>
                    {formatRelative(job.finished_at ?? job.started_at, locale)}
                </span>
            </span>
            {job.message && <Muted>{job.message}</Muted>}
        </>
    );
}

export default function HealthCard({ overview }: { overview: Overview }) {
    const { t, locale } = useT();
    const { status, summary } = overview;
    const spoolPercent = status.spool.limit_bytes > 0 ? (status.spool.used_bytes / status.spool.limit_bytes) * 100 : null;
    const running = status.maintenance.running_job;

    return (
        <Card title={t('health_title')}>
            <dl className={'peb:divide-y peb:divide-border'}>
                <Row label={t('health_gateway')}>
                    <span className={'peb:flex peb:flex-wrap peb:items-center peb:gap-2'}>
                        <Badge tone={'success'} variant={'soft'}>
                            {t('reachable')}
                        </Badge>
                        <Muted>{t('health_gateway_detail', { version: status.version, uptime: formatUptime(status.uptime_seconds) })}</Muted>
                    </span>
                </Row>
                <Row label={t('health_repo')}>
                    <span className={'peb:flex peb:flex-wrap peb:items-center peb:gap-2'}>
                        <Badge tone={status.repo.reachable ? 'success' : 'danger'} variant={'soft'}>
                            {status.repo.reachable ? t('reachable') : t('unreachable')}
                        </Badge>
                        <span className={'peb:font-mono peb:text-xs peb:break-all'}>{`${status.repo.type}: ${status.repo.location}`}</span>
                    </span>
                    <Muted>{t('checked', { time: formatRelative(status.repo.checked_at, locale) })}</Muted>
                </Row>
                <Row label={t('health_last_prune')}>
                    <JobRow job={status.maintenance.last_prune} />
                </Row>
                <Row label={t('health_last_check')}>
                    <JobRow job={status.maintenance.last_check} />
                </Row>
                {running && (
                    <Row label={t('health_running')}>
                        <span className={'peb:flex peb:items-center peb:gap-2'}>
                            <JobResultBadge result={running.result} />
                            <span>{t(`job_${running.type}`)}</span>
                        </span>
                    </Row>
                )}
                <Row label={t('health_next_window')}>
                    <span>
                        {formatDateTime(status.maintenance.next_window, locale)}{' '}
                        <Muted>({formatRelative(status.maintenance.next_window, locale)})</Muted>
                    </span>
                    {!status.maintenance.prune_enabled && <Muted>{t('prune_disabled')}</Muted>}
                </Row>
                <Row label={t('health_ingest')}>
                    {t('health_ingest_value', { running: status.ingest.running, queued: status.ingest.queued })}
                </Row>
                <Row label={t('health_spool')}>
                    <span>
                        {formatBytes(status.spool.used_bytes, locale)} / {formatBytes(status.spool.limit_bytes, locale)}
                    </span>
                    <ProgressBar value={spoolPercent} label={t('health_spool')} tone={spoolPercent !== null && spoolPercent > 80 ? 'warning' : 'neutral'} />
                </Row>
                <Row label={t('health_last_backup')}>
                    {summary.last_backup_at ? (
                        <span title={formatDateTime(summary.last_backup_at, locale)}>{formatRelative(summary.last_backup_at, locale)}</span>
                    ) : (
                        <Muted>{t('no_backups_yet')}</Muted>
                    )}
                </Row>
                <Row label={t('health_failures')}>
                    <span className={'peb:flex peb:flex-wrap peb:items-center peb:gap-2'}>
                        <Badge tone={summary.failures_30d > 0 ? 'danger' : 'success'} variant={'soft'}>
                            {formatNumber(summary.failures_30d, locale)}
                        </Badge>
                        <Muted>{t('health_success_rate', { rate: summary.success_rate_30d === null ? '–' : formatPercent(summary.success_rate_30d * 100, locale) })}</Muted>
                    </span>
                </Row>
            </dl>
        </Card>
    );
}

function formatUptime(seconds: number): string {
    const d = Math.floor(seconds / 86400);
    const h = Math.floor((seconds % 86400) / 3600);
    const m = Math.floor((seconds % 3600) / 60);

    return d > 0 ? `${d} d ${h} h` : h > 0 ? `${h} h ${m} min` : `${m} min`;
}

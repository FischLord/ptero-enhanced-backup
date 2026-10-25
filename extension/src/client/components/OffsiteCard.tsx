import type { ReactNode } from 'react';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { Alert, Badge, Button, Card, toast } from '@pterodactyl/sdk';
import { EXTENSION_ID, errorInfo, runMaintenance } from '../api';
import { formatBytes, formatDateTime, formatNumber, formatRelative } from '../format';
import { useT } from '../i18n';
import type { Overview, SecondaryStatus } from '../types';
import { Muted } from './Common';

export const OFFSITE_DOCS_URL = 'https://github.com/FischLord/ptero-enhanced-backup/blob/main/gateway/README.md#offsite-copy';

const LAG_WARN_MS = 24 * 3600 * 1000;

export interface OffsiteWarning {
    /** Der letzte Kopierlauf ist fehlgeschlagen. */
    failed: boolean;
    /** Snapshots fehlen seit mehr als 24 Stunden (Anzahl, sonst 0). */
    laggingFor24h: number;
}

/** Warnung fuer die Offsite-Kopie: letzter Lauf fehlgeschlagen oder Rueckstand laenger als 24 Stunden. */
export function offsiteWarning(s: SecondaryStatus, now: number = Date.now()): OffsiteWarning | null {
    const failed = s.state === 'error' || s.last_copy_result === 'failed' || s.last_error !== '';
    const since = s.lag_since ? new Date(s.lag_since).getTime() : NaN;
    const lag = s.lag_snapshots ?? 0;
    const laggingFor24h = lag > 0 && !Number.isNaN(since) && now - since > LAG_WARN_MS ? lag : 0;

    return failed || laggingFor24h > 0 ? { failed, laggingFor24h } : null;
}

/** Anzahl der in Folge uebersprungenen Prune-Laeufe, wenn das Gateway davor warnt, sonst 0. */
export function pruneSkipWarning(overview: Overview): number {
    const m = overview.status.maintenance;

    return (m.warnings?.length ?? 0) > 0 ? (m.prune_skipped_consecutive ?? 0) : 0;
}

function Row({ label, children }: { label: string; children: ReactNode }) {
    return (
        <div className={'peb:flex peb:flex-col peb:gap-0.5 peb:py-2 peb:sm:flex-row peb:sm:items-start peb:sm:gap-4'}>
            <dt className={'peb:text-sm peb:text-muted-foreground peb:sm:w-40 peb:sm:shrink-0'}>{label}</dt>
            <dd className={'peb:flex peb:min-w-0 peb:flex-1 peb:flex-col peb:gap-1 peb:text-sm peb:text-foreground'}>{children}</dd>
        </div>
    );
}

/** Zweitziel: Status, letzte Kopie, Rueckstand und belegter Platz der Offsite-Kopie. */
export default function OffsiteCard({ overview, now }: { overview: Overview; now?: number }) {
    const { t, locale } = useT();
    const queryClient = useQueryClient();
    const copyNow = useMutation({
        mutationFn: () => runMaintenance(['copy']),
        onSuccess: () => {
            toast.success(t('offsite_copy_started'));
            void queryClient.invalidateQueries({ queryKey: [EXTENSION_ID] });
        },
        onError: (error) => {
            const info = errorInfo(error);
            toast.error(info.code === 'busy' ? t('offsite_copy_busy') : info.message || t('error_unknown'));
        },
    });
    const s = overview.summary.secondary;
    const pruneSkipped = pruneSkipWarning(overview);
    const pruneAlert = pruneSkipped > 0 ? <Alert type={'warning'}>{t('offsite_warn_prune_skipped', { count: formatNumber(pruneSkipped, locale) })}</Alert> : null;

    if (!s || !s.configured) {
        return (
            <Card title={t('offsite_title')}>
                <div className={'peb:flex peb:flex-col peb:gap-2 peb:text-sm'}>
                    {pruneAlert}
                    <p>{t('offsite_off_note')}</p>
                    <Muted>{t('offsite_off_hint')}</Muted>
                    <a href={OFFSITE_DOCS_URL} target={'_blank'} rel={'noreferrer'} className={'peb:text-accent peb:hover:underline'}>
                        {t('offsite_docs_link')}
                    </a>
                </div>
            </Card>
        );
    }

    const warning = offsiteWarning(s, now);
    const tone = { ok: 'success', copying: 'info', pending: 'neutral', error: 'danger', off: 'neutral' } as const;
    const state = { ok: t('offsite_state_ok'), copying: t('offsite_state_copying'), pending: t('offsite_state_pending'), error: t('offsite_state_error'), off: '–' }[s.state];
    const lag = s.lag_snapshots;

    return (
        <Card title={t('offsite_title')}>
            <div className={'peb:flex peb:flex-col peb:gap-3'}>
                {warning?.laggingFor24h ? <Alert type={'warning'}>{t('offsite_warn_lag', { count: formatNumber(warning.laggingFor24h, locale) })}</Alert> : null}
                {warning?.failed ? <Alert type={'warning'}>{t('offsite_warn_failed')}</Alert> : null}
                {s.forget_blocked ? <Alert type={'warning'}>{t('offsite_warn_forget_blocked')}</Alert> : null}
                {pruneAlert}
                <dl className={'peb:divide-y peb:divide-border'}>
                    <Row label={t('offsite_status')}>
                        <span>
                            <Badge tone={tone[s.state]} variant={'soft'}>
                                {state}
                            </Badge>
                        </span>
                    </Row>
                    <Row label={t('offsite_last_copy')}>
                        {s.last_copy_at ? (
                            <span title={formatDateTime(s.last_copy_at, locale)}>{formatRelative(s.last_copy_at, locale, now)}</span>
                        ) : (
                            <Muted>{t('offsite_never')}</Muted>
                        )}
                    </Row>
                    <Row label={t('offsite_lag')}>
                        {lag === null ? (
                            <Muted>{t('offsite_lag_unknown')}</Muted>
                        ) : lag === 0 ? (
                            <span>{t('offsite_lag_none')}</span>
                        ) : (
                            <span>{t('offsite_lag_count', { count: formatNumber(lag, locale) })}</span>
                        )}
                    </Row>
                    <Row label={t('offsite_used')}>{s.stored_bytes === null ? <Muted>–</Muted> : <span title={formatDateTime(s.stored_at, locale)}>{formatBytes(s.stored_bytes, locale)}</span>}</Row>
                    <Row label={t('offsite_next')}>{s.next_copy_at ? <span title={formatDateTime(s.next_copy_at, locale)}>{formatRelative(s.next_copy_at, locale, now)}</span> : <Muted>–</Muted>}</Row>
                    {s.repo && (
                        <Row label={t('offsite_repo')}>
                            <span className={'peb:font-mono peb:text-xs peb:break-all'}>{s.repo}</span>
                        </Row>
                    )}
                    {s.last_error && (
                        <Row label={t('offsite_error')}>
                            <span className={'peb:break-words'}>{s.last_error}</span>
                        </Row>
                    )}
                </dl>
                <div className={'peb:flex peb:justify-end'}>
                    <Button size={'small'} isSecondary onClick={() => copyNow.mutate()} disabled={copyNow.isPending || s.running || s.state === 'copying'}>
                        {t('offsite_copy_now')}
                    </Button>
                </div>
            </div>
        </Card>
    );
}

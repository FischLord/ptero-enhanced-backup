import { Card, Tooltip } from '@pterodactyl/sdk';
import { formatBytes, formatNumber, formatPercent } from '../format';
import { useT } from '../i18n';
import type { Overview } from '../types';
import { Muted } from './Common';

function Legend({ color, label, value }: { color: string; label: string; value: string }) {
    return (
        <span className={'peb:flex peb:items-center peb:gap-2'}>
            <span aria-hidden className={`peb:size-3 peb:shrink-0 peb:rounded-sm ${color}`} />
            <span className={'peb:text-muted-foreground'}>{label}</span>
            <span className={'peb:font-medium peb:text-foreground'}>{value}</span>
        </span>
    );
}

/** Freier Platz auf dem Backup-Ziel: gestapelter Balken, Zahlen und Schaetzungen. */
export default function StorageTargetCard({ overview }: { overview: Overview }) {
    const { t, locale } = useT();
    const target = overview.summary.target;

    if (!target || target.total_bytes <= 0) {
        return (
            <Card title={t('target_title')}>
                <Muted>{t('target_empty')}</Muted>
            </Card>
        );
    }

    const est = target.exact ? '' : ` (${t('estimate_short')})`;
    const pct = (n: number) => `${Math.min(100, Math.max(0, (n / target.total_bytes) * 100))}%`;
    const usedPercent = (target.used_bytes / target.total_bytes) * 100;
    const days = target.days_until_full;
    const full =
        days === null ? t('target_full_unknown') : days <= 0 ? t('target_full_now') : t('target_full_in', { days: formatNumber(Math.ceil(days), locale) });
    const room =
        target.backups_remaining === null
            ? t('target_room_unknown')
            : t('target_room', { count: formatNumber(target.backups_remaining, locale) });

    return (
        <Card title={t('target_title')}>
            <Tooltip content={t(target.exact ? 'tip_target_exact' : 'tip_target_estimate')}>
                <div className={'peb:flex peb:cursor-help peb:flex-col peb:gap-4'} title={t(target.exact ? 'tip_target_exact' : 'tip_target_estimate')}>
                    <div
                        role={'img'}
                        aria-label={t('target_bar')}
                        className={'peb:flex peb:h-4 peb:w-full peb:overflow-hidden peb:rounded-full peb:bg-secondary'}
                    >
                        <div className={'peb:h-full peb:bg-success'} style={{ width: pct(target.peb_bytes) }} />
                        <div className={'peb:h-full peb:bg-muted-foreground'} style={{ width: pct(target.other_bytes) }} />
                    </div>
                    <div className={'peb:flex peb:flex-wrap peb:gap-x-6 peb:gap-y-1 peb:text-sm'}>
                        <Legend color={'peb:bg-success'} label={t('target_peb')} value={formatBytes(target.peb_bytes, locale)} />
                        <Legend color={'peb:bg-muted-foreground'} label={t('target_other')} value={formatBytes(target.other_bytes, locale)} />
                        <Legend color={'peb:bg-secondary'} label={t('target_free')} value={`${formatBytes(target.free_bytes, locale)}${est}`} />
                    </div>
                    <p className={'peb:text-sm'}>
                        {t('target_numbers', {
                            total: formatBytes(target.total_bytes, locale),
                            free: formatBytes(target.free_bytes, locale),
                            percent: formatPercent(usedPercent, locale),
                        })}
                        {est}
                    </p>
                    <ul className={'peb:flex peb:flex-col peb:gap-1 peb:text-sm'}>
                        <li>
                            {full}
                            {days !== null && est}
                        </li>
                        <li>
                            {room}
                            {target.backups_remaining !== null && est}
                        </li>
                    </ul>
                </div>
            </Tooltip>
        </Card>
    );
}

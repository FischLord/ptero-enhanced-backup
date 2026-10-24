import type { ReactNode } from 'react';
import type { LucideIcon } from 'lucide-react';
import { Coins, Database, HardDrive, PiggyBank, Scale } from 'lucide-react';
import { ProgressBar, Tooltip } from '@pterodactyl/sdk';
import { formatBytes, formatDateTime, formatMoney, formatNumber, formatPercent } from '../format';
import { useT } from '../i18n';
import type { Overview } from '../types';

/** Max. 3 Kacheln pro Zeile, mindestens 260 px breit; der Hauptwert wird nie abgeschnitten. */
const GRID_STYLE = { gridTemplateColumns: 'repeat(auto-fit, minmax(max(260px, calc((100% - 1.5rem) / 3)), 1fr))' };

function Tile({
    icon: Icon,
    label,
    value,
    hint,
    meter,
    success,
    unavailable,
}: {
    icon: LucideIcon;
    label: string;
    value: ReactNode;
    hint: string;
    meter?: number | null;
    success?: boolean;
    unavailable?: boolean;
}) {
    const tone = success && !unavailable ? 'success' : 'neutral';

    return (
        <Tooltip content={hint}>
            <div className={'peb:relative peb:flex peb:cursor-help peb:items-start peb:gap-4 peb:rounded-sm peb:bg-popover peb:p-4 peb:shadow-lg'} title={hint}>
                <div className={'peb:flex peb:size-12 peb:shrink-0 peb:items-center peb:justify-center peb:rounded-lg peb:bg-secondary peb:shadow-md'}>
                    <Icon aria-hidden className={'peb:size-6 peb:text-foreground'} />
                </div>
                <div className={'peb:flex peb:min-w-0 peb:flex-1 peb:flex-col peb:gap-1'}>
                    <p className={'peb:text-sm peb:font-medium peb:leading-tight peb:text-foreground'}>{label}</p>
                    <p
                        className={`peb:text-xl peb:font-semibold peb:leading-snug peb:break-words ${
                            unavailable ? 'peb:text-muted-foreground' : success ? 'peb:text-success' : 'peb:text-foreground'
                        }`}
                    >
                        {value}
                    </p>
                    {meter !== undefined && <ProgressBar value={meter} tone={tone} label={label} />}
                </div>
            </div>
        </Tooltip>
    );
}

export default function SavingsTiles({ overview }: { overview: Overview }) {
    const { t, locale } = useT();
    const { summary, savings } = overview;
    const exact = summary.stored_bytes_exact !== false;
    const hasStats = summary.stored_bytes !== null;
    const factor = summary.factor === null ? '' : formatNumber(summary.factor, locale, 1);
    const price = formatMoney(savings.price_per_tb, savings.currency, locale);

    return (
        <div className={'peb:flex peb:flex-col peb:gap-2'}>
            <div className={'peb:grid peb:gap-3'} style={GRID_STYLE}>
                <Tile icon={Database} label={t('tile_logical')} value={formatBytes(summary.logical_bytes, locale)} hint={t('tip_logical')} />
                <Tile
                    icon={HardDrive}
                    label={exact || !hasStats ? t('tile_stored') : t('tile_stored_approx')}
                    value={formatBytes(summary.stored_bytes, locale)}
                    hint={t('tip_stored')}
                    unavailable={!hasStats}
                />
                <Tile
                    icon={PiggyBank}
                    label={t('tile_saved')}
                    value={hasStats ? `${formatBytes(summary.saved_bytes, locale)} (${formatPercent(summary.saved_percent, locale)})` : '–'}
                    hint={t('tip_saved', { logical: formatBytes(summary.logical_bytes, locale), stored: formatBytes(summary.stored_bytes, locale) })}
                    meter={summary.saved_percent}
                    success
                    unavailable={!hasStats}
                />
                <Tile
                    icon={Scale}
                    label={t('tile_factor')}
                    value={summary.factor === null ? '–' : `${factor}×`}
                    hint={t('tip_factor', { factor })}
                    unavailable={summary.factor === null}
                />
                <Tile
                    icon={Coins}
                    label={exact || !hasStats ? t('tile_money') : t('tile_money_approx')}
                    value={formatMoney(savings.saved_per_month, savings.currency, locale)}
                    hint={t('tip_money', { price })}
                    success
                    unavailable={savings.saved_per_month === null}
                />
            </div>
            <p className={'peb:text-xs peb:text-muted-foreground'}>
                {t('tiles_footnote', {
                    count: formatNumber(summary.backups_count, locale),
                    price,
                    time: formatDateTime(summary.stored_at, locale),
                })}
                {hasStats && !exact && ` ${t('tiles_approx_note')}`}
            </p>
        </div>
    );
}

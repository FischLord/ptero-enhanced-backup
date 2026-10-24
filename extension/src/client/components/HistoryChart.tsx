import type { ReactNode } from 'react';
import { Card, ProgressBar } from '@pterodactyl/sdk';
import { formatBytes, formatDateTime, formatNumber, formatPercent } from '../format';
import { useT } from '../i18n';
import type { Forecast, History, HistoryDay } from '../types';

// Reines SVG ohne Bibliothek. Farben kommen aus den Theme-Variablen des Panels (hell und dunkel).
const W = 720;
const LEFT = 64;
const RIGHT = 12;
const TOP = 12;
const BOTTOM = 24;

const COLOR_STORED = 'var(--chart-1)';
const COLOR_LOGICAL = 'var(--chart-2)';
const COLOR_ADDED = 'var(--chart-3)';
const COLOR_FAILED = 'var(--destructive)';

/** Rundet die Obergrenze auf einen "schoenen" Wert (1, 2, 2.5, 5 x 10^n). */
export function niceMax(value: number): number {
    if (!(value > 0)) return 1;
    const exp = Math.pow(10, Math.floor(Math.log10(value)));
    const f = value / exp;
    const nice = f <= 1 ? 1 : f <= 2 ? 2 : f <= 2.5 ? 2.5 : f <= 5 ? 5 : 10;

    return nice * exp;
}

function shortDate(date: string, locale: string | undefined): string {
    const d = new Date(`${date}T12:00:00`);

    return Number.isNaN(d.getTime()) ? date : d.toLocaleDateString(locale, { day: 'numeric', month: 'numeric' });
}

function Frame({ height, label, children }: { height: number; label: string; children: ReactNode }) {
    return (
        <svg viewBox={`0 0 ${W} ${height}`} role={'img'} aria-label={label} className={'peb:h-auto peb:w-full'} style={{ color: 'var(--muted-foreground)' }}>
            {children}
        </svg>
    );
}

function YAxis({ max, height, locale }: { max: number; height: number; locale: string | undefined }) {
    const plotH = height - TOP - BOTTOM;

    return (
        <g fontSize={11} fill={'currentColor'}>
            {[0, 0.5, 1].map((f) => {
                const y = TOP + plotH - f * plotH;

                return (
                    <g key={f}>
                        <line x1={LEFT} x2={W - RIGHT} y1={y} y2={y} stroke={'var(--border)'} strokeWidth={1} />
                        <text x={LEFT - 6} y={y + 4} textAnchor={'end'}>
                            {formatBytes(max * f, locale)}
                        </text>
                    </g>
                );
            })}
        </g>
    );
}

function XAxis({ series, height, locale, xOf }: { series: HistoryDay[]; height: number; locale: string | undefined; xOf: (i: number) => number }) {
    const ticks = series.length <= 1 ? [0] : [0, Math.floor((series.length - 1) / 2), series.length - 1];

    return (
        <g fontSize={11} fill={'currentColor'}>
            {ticks.map((i, n) => (
                <text key={i} x={xOf(i)} y={height - 6} textAnchor={n === 0 ? 'start' : n === ticks.length - 1 ? 'end' : 'middle'}>
                    {shortDate(series[i].date, locale)}
                </text>
            ))}
        </g>
    );
}

function StoredChart({ series, locale, label }: { series: HistoryDay[]; locale: string | undefined; label: string }) {
    const H = 180;
    const plotW = W - LEFT - RIGHT;
    const plotH = H - TOP - BOTTOM;
    const values = series.map((d) => d.stored_bytes);
    const max = niceMax(Math.max(0, ...values.map((v) => v ?? 0)));
    const xOf = (i: number) => LEFT + (series.length <= 1 ? plotW / 2 : (i / (series.length - 1)) * plotW);
    const yOf = (v: number) => TOP + plotH - (v / max) * plotH;
    const points = values.map((v, i) => (v === null ? null : ([xOf(i), yOf(v)] as const)));

    // Luecken (keine Messung) trennen die Linie in Teilstuecke.
    const runs: (readonly [number, number])[][] = [];
    for (const p of points) {
        if (p === null) runs.push([]);
        else if (runs.length === 0) runs.push([p]);
        else runs[runs.length - 1].push(p);
    }
    const baseline = TOP + plotH;

    return (
        <Frame height={H} label={label}>
            <YAxis max={max} height={H} locale={locale} />
            <XAxis series={series} height={H} locale={locale} xOf={xOf} />
            {runs
                .filter((r) => r.length > 0)
                .map((run, i) => (
                    <g key={i}>
                        {run.length > 1 && (
                            <path
                                d={`M${run[0][0]},${baseline} ${run.map(([x, y]) => `L${x},${y}`).join(' ')} L${run[run.length - 1][0]},${baseline} Z`}
                                fill={COLOR_STORED}
                                opacity={0.15}
                            />
                        )}
                        <polyline points={run.map(([x, y]) => `${x},${y}`).join(' ')} fill={'none'} stroke={COLOR_STORED} strokeWidth={2} strokeLinejoin={'round'} />
                    </g>
                ))}
            {points.map((p, i) =>
                p === null ? null : (
                    <circle key={i} cx={p[0]} cy={p[1]} r={series.length > 45 ? 1.5 : 3} fill={COLOR_STORED}>
                        <title>{`${series[i].date}: ${formatBytes(values[i], locale)}`}</title>
                    </circle>
                ),
            )}
        </Frame>
    );
}

function DailyChart({ series, locale, label }: { series: HistoryDay[]; locale: string | undefined; label: string }) {
    const H = 180;
    const plotW = W - LEFT - RIGHT;
    const plotH = H - TOP - BOTTOM;
    const max = niceMax(Math.max(0, ...series.map((d) => Math.max(d.logical_bytes, d.data_added))));
    const slot = plotW / Math.max(1, series.length);
    const bar = Math.max(1, Math.min(14, (slot - 3) / 2));
    const center = (i: number) => LEFT + slot * i + slot / 2;
    const heightOf = (v: number) => (v / max) * plotH;

    return (
        <Frame height={H} label={label}>
            <YAxis max={max} height={H} locale={locale} />
            <XAxis series={series} height={H} locale={locale} xOf={center} />
            {series.map((d, i) => (
                <g key={d.date}>
                    <rect x={center(i) - bar} y={TOP + plotH - heightOf(d.logical_bytes)} width={bar} height={heightOf(d.logical_bytes)} fill={COLOR_LOGICAL} rx={1}>
                        <title>{`${d.date}: ${formatBytes(d.logical_bytes, locale)}`}</title>
                    </rect>
                    <rect x={center(i)} y={TOP + plotH - heightOf(d.data_added)} width={bar} height={heightOf(d.data_added)} fill={COLOR_ADDED} rx={1}>
                        <title>{`${d.date}: ${formatBytes(d.data_added, locale)}`}</title>
                    </rect>
                    {d.backups_failed > 0 && (
                        <circle cx={center(i)} cy={TOP + 4} r={3.5} fill={COLOR_FAILED}>
                            <title>{`${d.date}: ${d.backups_failed} ✗`}</title>
                        </circle>
                    )}
                </g>
            ))}
        </Frame>
    );
}

function Legend({ items }: { items: { color: string; label: string }[] }) {
    return (
        <ul className={'peb:flex peb:flex-wrap peb:gap-x-4 peb:gap-y-1 peb:text-xs peb:text-muted-foreground'}>
            {items.map((item) => (
                <li key={item.label} className={'peb:flex peb:items-center peb:gap-1.5'}>
                    <span aria-hidden className={'peb:inline-block peb:size-2.5 peb:rounded-sm'} style={{ background: item.color }} />
                    {item.label}
                </li>
            ))}
        </ul>
    );
}

function ForecastLine({ forecast }: { forecast: Forecast }) {
    const { t, locale } = useT();
    const percent = forecast.capacity_bytes > 0 ? (forecast.used_bytes / forecast.capacity_bytes) * 100 : null;
    let text: string;
    if (forecast.growth_bytes_per_day === null) text = t('forecast_unknown');
    else if (forecast.days_until_full === null) text = t('forecast_not_growing');
    else if (forecast.days_until_full <= 0) text = t('forecast_full');
    else
        text = t('forecast_full_in', {
            days: formatNumber(Math.ceil(forecast.days_until_full), locale),
            date: formatDateTime(forecast.full_at, locale),
        });

    return (
        <div className={'peb:flex peb:flex-col peb:gap-1.5 peb:rounded-md peb:border peb:border-border peb:p-3'}>
            <div className={'peb:flex peb:flex-wrap peb:justify-between peb:gap-2 peb:text-sm'}>
                <span className={'peb:font-medium peb:text-foreground'}>{text}</span>
                <span className={'peb:text-muted-foreground'}>
                    {formatBytes(forecast.used_bytes, locale)} / {formatBytes(forecast.capacity_bytes, locale)} ({formatPercent(percent, locale)})
                </span>
            </div>
            <ProgressBar value={percent} label={t('forecast_capacity')} tone={percent !== null && percent > 85 ? 'warning' : 'neutral'} />
            {forecast.growth_bytes_per_day !== null && (
                <span className={'peb:text-xs peb:text-muted-foreground'}>
                    {t('forecast_growth', { growth: formatBytes(forecast.growth_bytes_per_day, locale) })}
                </span>
            )}
        </div>
    );
}

export default function HistoryChart({ history }: { history: History }) {
    const { t, locale } = useT();
    const series = history.series;
    const hasData = series.some((d) => d.stored_bytes !== null || d.logical_bytes > 0 || d.data_added > 0);

    return (
        <Card title={t('chart_title', { days: history.days })}>
            {!hasData ? (
                <p className={'peb:text-sm peb:text-muted-foreground'}>{t('chart_empty')}</p>
            ) : (
                <div className={'peb:flex peb:flex-col peb:gap-5'}>
                    <div className={'peb:flex peb:flex-col peb:gap-2'}>
                        <h3 className={'peb:text-sm peb:font-medium peb:text-foreground'}>{t('chart_stored')}</h3>
                        <StoredChart series={series} locale={locale} label={t('chart_stored')} />
                    </div>
                    <div className={'peb:flex peb:flex-col peb:gap-2'}>
                        <h3 className={'peb:text-sm peb:font-medium peb:text-foreground'}>{t('chart_daily')}</h3>
                        <DailyChart series={series} locale={locale} label={t('chart_daily')} />
                        <Legend
                            items={[
                                { color: COLOR_LOGICAL, label: t('chart_legend_logical') },
                                { color: COLOR_ADDED, label: t('chart_legend_added') },
                                { color: COLOR_FAILED, label: t('chart_legend_failed') },
                            ]}
                        />
                    </div>
                </div>
            )}
            {history.forecast && (
                <div className={'peb:mt-4'}>
                    <ForecastLine forecast={history.forecast} />
                </div>
            )}
        </Card>
    );
}

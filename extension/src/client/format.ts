const UNITS = ['B', 'kB', 'MB', 'GB', 'TB', 'PB'];

/** Dezimale Einheiten (1 TB = 10^12 Byte), passend zum Preis pro TB. */
export function formatBytes(bytes: number | null | undefined, locale?: string): string {
    if (bytes === null || bytes === undefined || !Number.isFinite(bytes)) return '–';
    let value = Math.abs(bytes);
    let unit = 0;
    while (value >= 1000 && unit < UNITS.length - 1) {
        value /= 1000;
        unit++;
    }
    const digits = unit === 0 ? 0 : value >= 100 ? 0 : value >= 10 ? 1 : 2;
    const text = new Intl.NumberFormat(locale, { maximumFractionDigits: digits, minimumFractionDigits: 0 }).format(value);

    return `${bytes < 0 ? '-' : ''}${text} ${UNITS[unit]}`;
}

export function formatDuration(ms: number | null | undefined): string {
    if (ms === null || ms === undefined || !Number.isFinite(ms) || ms <= 0) return '–';
    const s = Math.round(ms / 1000);
    if (s < 1) return '<1 s';
    if (s < 60) return `${s} s`;
    const m = Math.floor(s / 60);
    if (m < 60) return `${m} min ${s % 60} s`;
    const h = Math.floor(m / 60);

    return `${h} h ${m % 60} min`;
}

export function formatDateTime(iso: string | null | undefined, locale?: string): string {
    if (!iso) return '–';
    const date = new Date(iso);

    return Number.isNaN(date.getTime()) ? '–' : date.toLocaleString(locale, { dateStyle: 'medium', timeStyle: 'short' });
}

/** "vor 3 Stunden" bzw. "in 5 hours", in der Sprache des Panels. */
export function formatRelative(iso: string | null | undefined, locale?: string, now: number = Date.now()): string {
    if (!iso) return '–';
    const time = new Date(iso).getTime();
    if (Number.isNaN(time)) return '–';
    const seconds = Math.round((time - now) / 1000);
    const abs = Math.abs(seconds);
    const rtf = new Intl.RelativeTimeFormat(locale, { numeric: 'auto' });
    if (abs < 60) return rtf.format(seconds, 'second');
    if (abs < 3600) return rtf.format(Math.round(seconds / 60), 'minute');
    if (abs < 86400) return rtf.format(Math.round(seconds / 3600), 'hour');

    return rtf.format(Math.round(seconds / 86400), 'day');
}

export function formatMoney(value: number | null | undefined, currency: string, locale?: string): string {
    if (value === null || value === undefined || !Number.isFinite(value)) return '–';
    try {
        return new Intl.NumberFormat(locale, { style: 'currency', currency }).format(value);
    } catch {
        return `${value.toFixed(2)} ${currency}`;
    }
}

export function formatPercent(value: number | null | undefined, locale?: string, digits = 1): string {
    if (value === null || value === undefined || !Number.isFinite(value)) return '–';

    return `${new Intl.NumberFormat(locale, { maximumFractionDigits: digits }).format(value)} %`;
}

export function formatNumber(value: number | null | undefined, locale?: string, digits = 0): string {
    if (value === null || value === undefined || !Number.isFinite(value)) return '–';

    return new Intl.NumberFormat(locale, { maximumFractionDigits: digits }).format(value);
}

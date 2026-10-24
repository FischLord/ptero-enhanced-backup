import { describe, expect, test } from 'vitest';
import { formatBytes, formatDuration, formatMoney, formatRelative } from '../../src/client/format';
import { niceMax } from '../../src/client/components/HistoryChart';
import de from '../../src/client/lang/de.json';
import en from '../../src/client/lang/en.json';

describe('format', () => {
    test('formats bytes in decimal units', () => {
        expect(formatBytes(0, 'en')).toBe('0 B');
        expect(formatBytes(1500, 'en')).toBe('1.5 kB');
        expect(formatBytes(1759143769, 'en')).toBe('1.76 GB');
        expect(formatBytes(1_099_511_627_776, 'en')).toBe('1.1 TB');
        expect(formatBytes(null)).toBe('–');
    });

    test('formats durations', () => {
        expect(formatDuration(0)).toBe('–');
        expect(formatDuration(18250)).toBe('18 s');
        expect(formatDuration(65_000)).toBe('1 min 5 s');
        expect(formatDuration(21_600_000)).toBe('6 h 0 min');
    });

    test('formats money and falls back for an invalid currency', () => {
        expect(formatMoney(18, 'EUR', 'en')).toBe('€18.00');
        expect(formatMoney(18, 'XX', 'en')).toBe('18.00 XX');
        expect(formatMoney(null, 'EUR')).toBe('–');
    });

    test('formats relative times against a fixed now', () => {
        const now = Date.parse('2026-10-24T12:00:00Z');
        expect(formatRelative('2026-10-24T09:00:00Z', 'en', now)).toBe('3 hours ago');
        expect(formatRelative('2026-10-26T12:00:00Z', 'en', now)).toBe('in 2 days');
        expect(formatRelative(null)).toBe('–');
    });

    test('rounds chart maxima to nice values', () => {
        expect(niceMax(0)).toBe(1);
        expect(niceMax(1759143769)).toBe(2e9);
        expect(niceMax(4.2e9)).toBe(5e9);
        expect(niceMax(2.3e9)).toBe(2.5e9);
    });
});

describe('translations', () => {
    test('German has exactly the keys of English and the same placeholders', () => {
        expect(Object.keys(de).sort()).toEqual(Object.keys(en).sort());
        const placeholders = (s: string) => [...s.matchAll(/\{\{\s*(\w+)\s*\}\}/g)].map((m) => m[1]).sort();
        for (const key of Object.keys(en) as (keyof typeof en)[]) {
            expect(placeholders(de[key]), key).toEqual(placeholders(en[key]));
        }
    });
});

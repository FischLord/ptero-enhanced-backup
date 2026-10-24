<?php

namespace PebBackups\Services;

use Carbon\CarbonImmutable;

/** Abgeleitete Werte, die das Gateway nicht liefert: eingesparte Kosten und Warnstufe pro Server. */
final class Insights
{
    private const BYTES_PER_TB = 1_000_000_000_000;

    /**
     * Gesparte Kosten pro Monat: gesparte Bytes (logisch minus gespeichert) in TB mal Preis pro TB.
     *
     * @param  array<string, mixed>  $summary  Antwort von /summary
     * @return array{currency: string, price_per_tb: float, saved_tb: float|null, saved_per_month: float|null, without_per_month: float|null, stored_per_month: float|null}
     */
    public static function savings(array $summary, float $pricePerTb, string $currency): array
    {
        $logical = self::int($summary['logical_bytes'] ?? null);
        $stored = self::int($summary['stored_bytes'] ?? null);
        $saved = self::int($summary['saved_bytes'] ?? null);

        $money = fn (?int $bytes): ?float => $bytes === null ? null : round($bytes / self::BYTES_PER_TB * $pricePerTb, 2);

        return [
            'currency' => $currency,
            'price_per_tb' => $pricePerTb,
            'saved_tb' => $saved === null ? null : round($saved / self::BYTES_PER_TB, 4),
            'saved_per_month' => $money($saved),
            'without_per_month' => $money($logical),
            'stored_per_month' => $money($stored),
        ];
    }

    /**
     * Zustand eines Servers: ok | warning | failed | never.
     *  - failed: das juengste beendete Backup ist fehlgeschlagen
     *  - never: noch nie ein erfolgreiches Backup
     *  - warning: letztes erfolgreiches Backup aelter als $warnHours
     *
     * @param  array<string, mixed>  $server  Zeile von /servers
     */
    public static function serverHealth(array $server, int $warnHours, ?CarbonImmutable $now = null): string
    {
        $now ??= CarbonImmutable::now();
        $lastSuccess = self::date($server['last_success_at'] ?? null);

        if (($server['last_status'] ?? null) === 'failed') {
            return 'failed';
        }
        if ($lastSuccess === null) {
            return 'never';
        }
        if ($lastSuccess->lt($now->subHours($warnHours))) {
            return 'warning';
        }

        return 'ok';
    }

    /** Unlesbare Zeitangaben gelten als fehlend. */
    private static function date(mixed $value): ?CarbonImmutable
    {
        if (! is_string($value) || $value === '') {
            return null;
        }
        try {
            return CarbonImmutable::parse($value);
        } catch (\Throwable) {
            return null;
        }
    }

    private static function int(mixed $value): ?int
    {
        return is_int($value) || is_float($value) ? (int) $value : null;
    }
}

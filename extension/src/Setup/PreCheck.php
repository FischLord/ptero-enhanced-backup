<?php

namespace PebBackups\Setup;

/**
 * Bewertet die Backup-Konfiguration des Panels. Reine Funktion ohne Panel-Zugriff.
 * Die Ausgabe enthaelt nie Werte von Geheimnissen oder Adressen, nur Status.
 */
final class PreCheck
{
    public const MIB = 1048576;
    /** Unterhalb davon entstehen sehr viele Teile (S3 verlangt mind. 5 MiB, ausser fuer das letzte). */
    public const PART_MIN = 16 * self::MIB;
    /** Das Gateway lehnt groessere Teile ab (PEB_MAX_PART_BYTES, Standard 1 GiB). */
    public const PART_MAX = 1073741824;

    /**
     * @param  array{driver?: mixed, endpoint?: mixed, path_style?: mixed, max_part_size?: mixed, bucket?: mixed, key?: mixed, secret?: mixed, gateway_url?: mixed, admin_token?: mixed}  $facts
     * @return array{ready: bool, checks: list<array{id: string, status: 'ok'|'warn'|'fail', hint: string|null, value: int|null}>}
     */
    public static function evaluate(array $facts): array
    {
        $set = fn (string $key): bool => is_string($facts[$key] ?? null) && trim($facts[$key]) !== '';
        $part = (int) ($facts['max_part_size'] ?? 0);

        $checks = [
            self::check('driver', ($facts['driver'] ?? null) === 's3', 'driver_not_s3'),
            self::check('endpoint', $set('endpoint'), 'endpoint_missing'),
            self::check('path_style', ($facts['path_style'] ?? false) === true, 'path_style_off'),
            self::check('credentials', $set('bucket') && $set('key') && $set('secret'), 'credentials_missing'),
            self::partSize($part),
            self::check('gateway_url', $set('gateway_url'), 'gateway_url_missing'),
            self::check('admin_token', $set('admin_token'), 'admin_token_missing'),
        ];

        return [
            'ready' => ! array_filter($checks, fn (array $c): bool => $c['status'] === 'fail'),
            'checks' => $checks,
        ];
    }

    /** @return array{id: string, status: 'ok'|'fail', hint: string|null, value: null} */
    private static function check(string $id, bool $ok, string $hint): array
    {
        return ['id' => $id, 'status' => $ok ? 'ok' : 'fail', 'hint' => $ok ? null : $hint, 'value' => null];
    }

    /** @return array{id: string, status: 'ok'|'warn'|'fail', hint: string|null, value: int|null} */
    private static function partSize(int $bytes): array
    {
        if ($bytes <= 0) {
            return ['id' => 'part_size', 'status' => 'fail', 'hint' => 'part_size_invalid', 'value' => null];
        }
        if ($bytes < self::PART_MIN) {
            return ['id' => 'part_size', 'status' => 'warn', 'hint' => 'part_size_small', 'value' => $bytes];
        }
        if ($bytes > self::PART_MAX) {
            return ['id' => 'part_size', 'status' => 'warn', 'hint' => 'part_size_large', 'value' => $bytes];
        }

        return ['id' => 'part_size', 'status' => 'ok', 'hint' => null, 'value' => $bytes];
    }
}

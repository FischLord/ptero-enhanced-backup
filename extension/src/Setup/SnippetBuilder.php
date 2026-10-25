<?php

namespace PebBackups\Setup;

/**
 * Baut die Textbausteine zum Kopieren. Die .env wird nie automatisch geschrieben.
 * Fehlende Werte erscheinen als <Platzhalter>.
 */
final class SnippetBuilder
{
    public const DEFAULT_PART_SIZE = 268435456;

    /** @param array{gateway_url?: ?string, region?: ?string, bucket?: ?string, access_key?: ?string, secret_key?: ?string, max_part_size?: ?int} $v */
    public static function env(array $v): string
    {
        $or = fn (string $key, string $placeholder): string => ($v[$key] ?? '') !== '' && $v[$key] !== null ? (string) $v[$key] : $placeholder;

        return implode("\n", [
            'APP_BACKUP_DRIVER=s3',
            'AWS_ENDPOINT='.$or('gateway_url', '<gateway address>'),
            'AWS_USE_PATH_STYLE_ENDPOINT=true',
            'AWS_DEFAULT_REGION='.$or('region', 'us-east-1'),
            'AWS_BACKUPS_BUCKET='.$or('bucket', '<bucket>'),
            'AWS_ACCESS_KEY_ID='.$or('access_key', '<access key>'),
            'AWS_SECRET_ACCESS_KEY='.$or('secret_key', '<secret>'),
            'BACKUP_MAX_PART_SIZE='.(($v['max_part_size'] ?? null) ?: self::DEFAULT_PART_SIZE),
        ])."\n";
    }

    public static function wings(?string $gatewayUrl): string
    {
        $host = $gatewayUrl ? parse_url($gatewayUrl, PHP_URL_HOST) : null;
        $host = is_string($host) && $host !== '' ? $host : '<gateway host or IP>';

        return implode("\n", [
            'system:',
            '  backups:',
            '    compression_level: none',
            '    restore_host_allowlist:',
            '      - '.$host,
        ])."\n";
    }

    /** Adresse des Gateways aus einer Allocation (IP-Alias, sonst IP). IPv6 wird eingeklammert. */
    public static function gatewayUrl(string $host, int $port, bool $https = false): string
    {
        if (str_contains($host, ':') && ! str_starts_with($host, '[')) {
            $host = '['.$host.']';
        }

        return ($https ? 'https' : 'http').'://'.$host.':'.$port;
    }
}

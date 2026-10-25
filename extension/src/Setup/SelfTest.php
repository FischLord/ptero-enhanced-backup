<?php

namespace PebBackups\Setup;

use Illuminate\Http\Client\ConnectionException;
use Illuminate\Http\Client\Factory;
use PebBackups\Gateway\GatewayClient;
use PebBackups\Gateway\GatewayConfig;
use PebBackups\Gateway\GatewayException;
use Ramsey\Uuid\Uuid;
use Throwable;

/**
 * Selbsttest: Gateway erreichbar, Admin-Token gueltig, signierte S3-Anfrage mit den Panel-Zugangsdaten,
 * HTTPS-Hinweis. Jeder Schritt liefert ok, warn oder fail und einen Hinweis-Code (nie Geheimnisse).
 */
final class SelfTest
{
    public function __construct(
        private readonly Factory $http,
        private readonly GatewayConfig $config,
        private readonly S3Prober $s3,
        private readonly ?string $s3Endpoint = null,
    ) {}

    /** @return array{ok: bool, checks: list<array{id: string, status: 'ok'|'warn'|'fail', hint: string|null}>} */
    public function run(): array
    {
        $gateway = $this->gateway();
        $checks = [
            $gateway,
            $this->token($gateway['status'] === 'fail' ? $gateway['hint'] : null),
            $this->s3(),
            $this->https(),
        ];

        return ['ok' => ! array_filter($checks, fn (array $c): bool => $c['status'] === 'fail'), 'checks' => $checks];
    }

    /** @return array{id: string, status: 'ok'|'warn'|'fail', hint: string|null} */
    private static function result(string $id, string $status, ?string $hint = null): array
    {
        return ['id' => $id, 'status' => $status, 'hint' => $hint];
    }

    private function gateway(): array
    {
        if ($this->config->baseUrl === '') {
            return self::result('gateway', 'fail', 'gateway_url_missing');
        }

        try {
            $response = $this->http
                ->withoutRedirecting()
                ->connectTimeout($this->config->connectTimeoutSeconds)
                ->timeout($this->config->timeoutSeconds)
                ->get($this->config->baseUrl.'/healthz');
        } catch (ConnectionException $e) {
            $timeout = str_contains($e->getMessage(), 'cURL error 28') || stripos($e->getMessage(), 'timed out') !== false;

            return self::result('gateway', 'fail', $timeout ? 'gateway_timeout' : 'gateway_unreachable');
        } catch (Throwable) {
            return self::result('gateway', 'fail', 'gateway_unreachable');
        }

        return $response->successful() && str_starts_with(trim($response->body()), 'ok')
            ? self::result('gateway', 'ok')
            : self::result('gateway', 'fail', 'gateway_not_peb');
    }

    private function token(?string $gatewayFailure): array
    {
        if ($this->config->token === '') {
            return self::result('token', 'fail', 'admin_token_missing');
        }
        if ($gatewayFailure !== null) {
            // Das Gateway antwortet ohnehin nicht; keine zweite Wartezeit.
            return self::result('token', 'fail', $gatewayFailure === 'gateway_url_missing' ? 'gateway_url_missing' : 'gateway_first');
        }

        try {
            (new GatewayClient($this->http, $this->config))->get('status');
        } catch (GatewayException $e) {
            return self::result('token', 'fail', match ($e->errorCode) {
                'gateway_unauthorized' => 'token_rejected',
                'api_disabled' => 'api_disabled',
                'gateway_timeout' => 'gateway_timeout',
                default => 'gateway_unreachable',
            });
        } catch (Throwable) {
            return self::result('token', 'fail', 'gateway_unreachable');
        }

        return self::result('token', 'ok');
    }

    private function s3(): array
    {
        $key = Uuid::uuid4()->toString().'/'.Uuid::uuid4()->toString().'.tar.gz';

        try {
            $uploadId = $this->s3->create($key);
        } catch (SetupException $e) {
            return self::result('s3', 'fail', $e->errorCode);
        } catch (Throwable) {
            return self::result('s3', 'fail', 's3_error');
        }

        try {
            $this->s3->abort($key, $uploadId);
        } catch (Throwable) {
            // Der Test-Upload konnte nicht abgebrochen werden und liegt eventuell noch im Spool des Gateways.
            return self::result('s3', 'warn', 's3_abort_failed');
        }

        return self::result('s3', 'ok');
    }

    private function https(): array
    {
        $urls = array_filter([$this->s3Endpoint, $this->config->baseUrl], fn ($u) => is_string($u) && $u !== '');
        if ($urls === []) {
            return self::result('https', 'fail', 'gateway_url_missing');
        }
        foreach ($urls as $url) {
            if (! str_starts_with(strtolower($url), 'https://')) {
                return self::result('https', 'warn', 'not_https');
            }
        }

        return self::result('https', 'ok');
    }
}

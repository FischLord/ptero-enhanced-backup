<?php

namespace PebBackups\Gateway;

/** Die Einstellungen der Extension als unveraenderlicher Wert (leicht zu testen). */
final class GatewayConfig
{
    public function __construct(
        public readonly string $baseUrl,
        public readonly string $token,
        public readonly float $pricePerTb = 6.0,
        public readonly string $currency = 'EUR',
        public readonly int $warnHours = 26,
        public readonly int $timeoutSeconds = 8,
        public readonly int $connectTimeoutSeconds = 5,
        public readonly int $downloadTimeoutSeconds = 7200,
    ) {}

    /** @param callable(string): mixed $get */
    public static function fromSettings(callable $get): self
    {
        return new self(
            baseUrl: rtrim((string) ($get('gateway_url') ?? ''), '/'),
            token: (string) ($get('admin_token') ?? ''),
            pricePerTb: (float) ($get('price_per_tb') ?? 6),
            currency: strtoupper((string) ($get('currency') ?: 'EUR')),
            warnHours: max(1, (int) ($get('warn_hours') ?? 26)),
        );
    }

    public function isConfigured(): bool
    {
        return $this->baseUrl !== '' && $this->token !== '';
    }

    /** Basis aller Admin-Aufrufe, z. B. http://host:9000/admin/v1 */
    public function apiBase(): string
    {
        return $this->baseUrl.'/admin/v1';
    }
}

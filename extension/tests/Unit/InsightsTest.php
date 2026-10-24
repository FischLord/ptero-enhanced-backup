<?php

use Carbon\CarbonImmutable;
use PebBackups\Gateway\GatewayConfig;
use PebBackups\Services\Insights;

test('computes the money saved per month from the saved bytes', function () {
    $s = Insights::savings([
        'logical_bytes' => 4_000_000_000_000,
        'stored_bytes' => 1_000_000_000_000,
        'saved_bytes' => 3_000_000_000_000,
    ], 6.0, 'EUR');

    expect($s['saved_per_month'])->toBe(18.0)
        ->and($s['without_per_month'])->toBe(24.0)
        ->and($s['stored_per_month'])->toBe(6.0)
        ->and($s['saved_tb'])->toBe(3.0)
        ->and($s['currency'])->toBe('EUR');
});

test('stays null while the gateway has no repository statistics yet', function () {
    $s = Insights::savings(['logical_bytes' => 1000, 'stored_bytes' => null, 'saved_bytes' => null], 6.0, 'EUR');

    expect($s['saved_per_month'])->toBeNull()->and($s['stored_per_month'])->toBeNull();
});

test('classifies the server health', function () {
    $now = CarbonImmutable::parse('2026-10-24T12:00:00Z');

    expect(Insights::serverHealth(['last_status' => 'complete', 'last_success_at' => '2026-10-24T03:00:00Z'], 26, $now))->toBe('ok')
        ->and(Insights::serverHealth(['last_status' => 'complete', 'last_success_at' => '2026-10-22T03:00:00Z'], 26, $now))->toBe('warning')
        ->and(Insights::serverHealth(['last_status' => 'failed', 'last_success_at' => '2026-10-24T03:00:00Z'], 26, $now))->toBe('failed')
        ->and(Insights::serverHealth(['last_status' => 'failed', 'last_success_at' => null], 26, $now))->toBe('failed')
        ->and(Insights::serverHealth(['last_status' => null, 'last_success_at' => null], 26, $now))->toBe('never');
});

test('the warning threshold is exclusive at exactly N hours', function () {
    $now = CarbonImmutable::parse('2026-10-24T12:00:00Z');

    expect(Insights::serverHealth(['last_status' => 'complete', 'last_success_at' => '2026-10-23T10:00:00Z'], 26, $now))->toBe('ok')
        ->and(Insights::serverHealth(['last_status' => 'complete', 'last_success_at' => '2026-10-23T09:59:59Z'], 26, $now))->toBe('warning');
});

test('builds the config from stored settings with defaults and normalisation', function () {
    $c = GatewayConfig::fromSettings(fn ($k) => ['gateway_url' => 'http://x:9000/', 'admin_token' => 't', 'currency' => 'usd', 'price_per_tb' => '7.5', 'warn_hours' => '0'][$k] ?? null);

    expect($c->baseUrl)->toBe('http://x:9000')
        ->and($c->apiBase())->toBe('http://x:9000/admin/v1')
        ->and($c->currency)->toBe('USD')
        ->and($c->pricePerTb)->toBe(7.5)
        ->and($c->warnHours)->toBe(1)
        ->and($c->isConfigured())->toBeTrue();

    expect(GatewayConfig::fromSettings(fn () => null)->isConfigured())->toBeFalse();
});

test('an unparsable date counts as missing instead of throwing', function () {
    $now = CarbonImmutable::parse('2026-10-24T12:00:00Z');

    expect(Insights::serverHealth(['last_status' => 'complete', 'last_success_at' => 'not a date'], 26, $now))->toBe('never');
});

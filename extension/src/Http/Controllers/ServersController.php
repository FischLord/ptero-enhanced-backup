<?php

namespace PebBackups\Http\Controllers;

use Illuminate\Http\JsonResponse;
use PebBackups\Gateway\GatewayClient;
use PebBackups\Services\Insights;
use PebBackups\Services\ServerEnricher;

class ServersController
{
    public function __invoke(GatewayClient $gateway, ServerEnricher $enricher): JsonResponse
    {
        $warnHours = $gateway->config()->warnHours;
        $items = $gateway->get('servers')['items'] ?? [];

        $items = array_map(function (array $row) use ($warnHours): array {
            $row['health'] = Insights::serverHealth($row, $warnHours);

            return $row;
        }, array_values((array) $items));

        return (new JsonResponse([
            'items' => $enricher->attach($items),
            'warn_hours' => $warnHours,
            'generated_at' => now()->toAtomString(),
        ]))->header('Cache-Control', 'no-store');
    }
}

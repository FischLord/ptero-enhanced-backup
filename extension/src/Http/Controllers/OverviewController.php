<?php

namespace PebBackups\Http\Controllers;

use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use PebBackups\Gateway\GatewayClient;
use PebBackups\Services\Insights;
use PebBackups\Services\ServerReconciler;

class OverviewController
{
    /** Status, Kennzahlen und Tagesverlauf in einem Aufruf, dazu die berechneten Einsparungen. */
    public function __invoke(Request $request, GatewayClient $gateway, ServerReconciler $reconciler): JsonResponse
    {
        $data = $request->validate(['days' => ['sometimes', 'integer', 'min:1', 'max:365']]);
        $config = $gateway->config();
        // Geloeschte Server sofort erkennen, auch wenn das Ereignis oder der Stundenlauf fehlte (hoechstens alle 5 Minuten).
        $reconciler->reconcileQuietly();

        ['status' => $status, 'summary' => $summary, 'history' => $history] = $gateway->getMany([
            'status' => ['status'],
            'summary' => ['summary'],
            'history' => ['history', ['days' => $data['days'] ?? 30]],
        ]);

        return $this->json([
            'status' => $status,
            'summary' => $summary,
            'history' => $history,
            'savings' => Insights::savings($summary, $config->pricePerTb, $config->currency),
            'settings' => [
                'currency' => $config->currency,
                'price_per_tb' => $config->pricePerTb,
                'warn_hours' => $config->warnHours,
            ],
            'generated_at' => now()->toAtomString(),
        ]);
    }

    private function json(array $data): JsonResponse
    {
        return (new JsonResponse($data))->header('Cache-Control', 'no-store');
    }
}

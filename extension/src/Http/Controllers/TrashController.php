<?php

namespace PebBackups\Http\Controllers;

use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Support\Facades\Log;
use PebBackups\Gateway\GatewayClient;
use PebBackups\Services\ServerEnricher;

/** Papierkorb des Gateways (docs/admin-api.md): Liste, Wiederherstellen, endgueltig loeschen. */
class TrashController
{
    public function index(GatewayClient $gateway, ServerEnricher $enricher): JsonResponse
    {
        $result = $gateway->get('trash');
        $result['items'] = $enricher->attach(array_values((array) ($result['items'] ?? [])));

        return $this->json($result);
    }

    /** Macht das Backup wieder zu einem normalen Backup des Gateways (fuers Panel bleibt es unbekannt). */
    public function restore(Request $request, string $backup, GatewayClient $gateway): JsonResponse
    {
        $result = $gateway->post("trash/{$backup}/restore", []);
        Log::info('PEB backup restored from the trash.', ['user_id' => $request->user()?->id, 'backup' => $backup]);

        return $this->json($result);
    }

    /** Loescht sofort und endgueltig. Verlangt `confirm: true`, damit kein Aufruf aus Versehen laeuft. */
    public function purge(Request $request, string $backup, GatewayClient $gateway): JsonResponse
    {
        $request->validate(['confirm' => ['required', 'accepted']]);
        $result = $gateway->delete("trash/{$backup}");
        Log::info('PEB backup purged from the trash.', ['user_id' => $request->user()?->id, 'backup' => $backup]);

        return $this->json($result);
    }

    private function json(array $data): JsonResponse
    {
        return (new JsonResponse($data))->header('Cache-Control', 'no-store');
    }
}

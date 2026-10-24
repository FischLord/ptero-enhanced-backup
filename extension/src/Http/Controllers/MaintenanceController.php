<?php

namespace PebBackups\Http\Controllers;

use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Support\Facades\Log;
use PebBackups\Gateway\GatewayClient;

class MaintenanceController
{
    public function index(Request $request, GatewayClient $gateway): JsonResponse
    {
        $data = $request->validate(['limit' => ['sometimes', 'integer', 'min:1', 'max:500']]);

        return (new JsonResponse($gateway->get('maintenance', ['limit' => $data['limit'] ?? 50])))->header('Cache-Control', 'no-store');
    }

    /** Startet Wartungsjobs sofort. Verlangt `confirm: true`, damit kein Aufruf aus Versehen laeuft. */
    public function run(Request $request, GatewayClient $gateway): JsonResponse
    {
        $data = $request->validate([
            'confirm' => ['required', 'accepted'],
            'jobs' => ['required', 'array', 'min:1', 'max:3'],
            'jobs.*' => ['string', 'in:prune,check,stats', 'distinct'],
        ]);

        $result = $gateway->post('maintenance/run', ['jobs' => array_values($data['jobs'])]);

        Log::info('PEB maintenance run started from the panel.', [
            'user_id' => $request->user()?->id,
            'jobs' => $data['jobs'],
            'job_ids' => $result['job_ids'] ?? [],
        ]);

        return (new JsonResponse($result, 202))->header('Cache-Control', 'no-store');
    }
}

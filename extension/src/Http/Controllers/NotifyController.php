<?php

namespace PebBackups\Http\Controllers;

use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Support\Facades\Log;
use PebBackups\Gateway\GatewayClient;
use PebBackups\Gateway\GatewayException;

/**
 * Benachrichtigungen des Gateways: Kanaele (vom Gateway bereits geschwaerzt) und Testnachricht.
 * Konfiguriert wird ausschliesslich ueber Egg-Variablen am Gateway. Gateways vor 0.3 kennen die
 * Routen nicht; das wird als 404 "not_supported" gemeldet, damit die Oberflaeche einen Hinweis zeigt.
 */
class NotifyController
{
    public function index(GatewayClient $gateway): JsonResponse
    {
        return $this->json($this->guard(fn (): array => $gateway->get('notify')));
    }

    public function test(Request $request, GatewayClient $gateway): JsonResponse
    {
        $result = $this->guard(fn (): array => $gateway->post('notify/test', []));

        Log::info('PEB test notification sent from the panel.', ['user_id' => $request->user()?->id]);

        return $this->json($result);
    }

    /**
     * @param  callable(): array<string, mixed>  $call
     * @return array<string, mixed>
     */
    private function guard(callable $call): array
    {
        try {
            return $call();
        } catch (GatewayException $e) {
            // Altes Gateway: Route unbekannt (404 ohne JSON-Koerper) oder 404 mit Fehlercode.
            if (in_array($e->errorCode, ['api_disabled', 'not_found'], true)) {
                throw new GatewayException('not_supported', 'This gateway does not support notifications yet. Update the gateway to 0.3 or newer.', 404);
            }
            throw $e;
        }
    }

    /** @param array<string, mixed> $data */
    private function json(array $data, int $status = 200): JsonResponse
    {
        return (new JsonResponse($data, $status))->header('Cache-Control', 'no-store');
    }
}

<?php

namespace PebBackups\Http\Controllers;

use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Support\Facades\Log;
use PebBackups\Gateway\GatewayClient;
use PebBackups\Services\ServerEnricher;

/**
 * Schutz des Restic-Passworts: Bestaetigung "an zweitem Ort gesichert", Daten fuer das Notfallblatt
 * und zweite restic-Schluessel. Alles Proxys zur Admin-API; das Panel speichert nichts davon.
 */
class SecurityController
{
    public function passwordAck(GatewayClient $gateway): JsonResponse
    {
        return $this->json($gateway->get('settings/password-ack'));
    }

    public function setPasswordAck(Request $request, GatewayClient $gateway): JsonResponse
    {
        $data = $request->validate(['acknowledged' => ['required', 'boolean']]);
        $user = $request->user();
        $by = (string) ($user?->username ?? $user?->email ?? '');

        $result = $gateway->put('settings/password-ack', ['acknowledged' => (bool) $data['acknowledged'], 'by' => $by]);

        Log::info('PEB restic password acknowledgement changed from the panel.', ['user_id' => $user?->id, 'acknowledged' => (bool) $data['acknowledged']]);

        return $this->json($result);
    }

    public function keys(GatewayClient $gateway): JsonResponse
    {
        return $this->json($gateway->get('keys'));
    }

    /** Erzeugt einen zweiten Schluessel. Die Antwort enthaelt das neue Passwort genau einmal; es wird nicht geloggt. */
    public function createKey(Request $request, GatewayClient $gateway): JsonResponse
    {
        $data = $request->validate(['label' => ['sometimes', 'nullable', 'string', 'max:64']]);

        $result = $gateway->post('keys', ['label' => (string) ($data['label'] ?? '')]);

        Log::info('PEB emergency key created from the panel.', ['user_id' => $request->user()?->id, 'key' => $result['short_id'] ?? null]);

        return $this->json($result, 201);
    }

    public function deleteKey(Request $request, GatewayClient $gateway, string $key): JsonResponse
    {
        $gateway->delete('keys/'.$key);

        Log::info('PEB restic key removed from the panel.', ['user_id' => $request->user()?->id, 'key' => substr($key, 0, 8)]);

        return $this->json(['ok' => true]);
    }

    /** Alles, was das Notfallblatt braucht, ausser dem Passwort (das das Panel nie kennt). */
    public function emergencySheet(GatewayClient $gateway, ServerEnricher $enricher): JsonResponse
    {
        ['status' => $status, 'summary' => $summary, 'servers' => $servers] = $gateway->getMany([
            'status' => ['status'],
            'summary' => ['summary'],
            'servers' => ['servers'],
        ]);

        $items = $enricher->attach(array_values((array) ($servers['items'] ?? [])));
        $list = array_map(fn (array $row): array => [
            'uuid' => (string) ($row['server_uuid'] ?? ''),
            'name' => $row['server']['name'] ?? null,
        ], $items);

        return $this->json([
            'gateway_url' => $gateway->config()->baseUrl,
            'repo' => ['type' => $status['repo']['type'] ?? '', 'location' => $status['repo']['location'] ?? ''],
            'secondary' => ($summary['secondary']['configured'] ?? false)
                ? ['type' => $summary['secondary']['type'] ?? '', 'repo' => $summary['secondary']['repo'] ?? '']
                : null,
            'servers' => $list,
            'generated_at' => now()->toAtomString(),
        ]);
    }

    /** @param array<string, mixed> $data */
    private function json(array $data, int $status = 200): JsonResponse
    {
        return (new JsonResponse($data, $status))->header('Cache-Control', 'no-store');
    }
}

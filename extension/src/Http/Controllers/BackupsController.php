<?php

namespace PebBackups\Http\Controllers;

use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Support\Facades\Log;
use Illuminate\Support\Facades\Validator;
use PebBackups\Gateway\GatewayClient;
use PebBackups\Gateway\GatewayException;
use PebBackups\Services\ServerEnricher;
use Symfony\Component\HttpFoundation\StreamedResponse;
use Throwable;

class BackupsController
{
    /** Backups, optional eines Servers (`server` = UUID). */
    public function index(Request $request, GatewayClient $gateway, ServerEnricher $enricher): JsonResponse
    {
        $data = $request->validate([
            'server' => ['sometimes', 'uuid'],
            'status' => ['sometimes', 'in:complete,failed,pending,deleted'],
            'limit' => ['sometimes', 'integer', 'min:1', 'max:500'],
            'offset' => ['sometimes', 'integer', 'min:0'],
        ]);

        $result = $gateway->get('backups', $data);
        $result['items'] = $enricher->attach(array_values((array) ($result['items'] ?? [])));

        return (new JsonResponse($result))->header('Cache-Control', 'no-store');
    }

    /** Verzeichnisansicht aus dem Dateiindex eines Backups. */
    public function files(Request $request, string $backup, GatewayClient $gateway): JsonResponse
    {
        $data = $request->validate([
            'limit' => ['sometimes', 'integer', 'min:1', 'max:1000'],
            'offset' => ['sometimes', 'integer', 'min:0'],
        ]);
        $data['path'] = self::rawPath($request) ?? '';
        $this->validatePath($data['path'], false);

        return (new JsonResponse($gateway->get("backups/{$backup}/files", $data)))->header('Cache-Control', 'no-store');
    }

    /**
     * Einzelne Datei, vom Gateway durchgereicht (der Token bleibt im Panel).
     * Mit `probe=1` nur pruefen (Sitzung, Backup fertig, Datei im Index), ohne Daten zu holen:
     * Die Oberflaeche prueft so vor dem Download, damit der Browser keine JSON-Fehler als Datei speichert.
     */
    public function download(Request $request, string $backup, GatewayClient $gateway): StreamedResponse|JsonResponse
    {
        $path = self::rawPath($request) ?? '';
        $this->validatePath($path, true);

        if ($request->boolean('probe')) {
            return $this->probe($backup, $path, $gateway);
        }

        $upstream = $gateway->stream("backups/{$backup}/file", ['path' => $path]);
        $body = $upstream->toPsrResponse()->getBody();

        $headers = [
            'Content-Type' => 'application/octet-stream',
            'Cache-Control' => 'no-store',
            'X-Content-Type-Options' => 'nosniff',
        ];
        // Mit Content-Length erkennt der Browser einen abgebrochenen Strom als unvollstaendig.
        if (ctype_digit((string) $upstream->header('Content-Length'))) {
            $headers['Content-Length'] = $upstream->header('Content-Length');
        }

        return response()->streamDownload(function () use ($body, $backup): void {
            try {
                while (! $body->eof()) {
                    if (connection_aborted()) {
                        break;
                    }
                    echo $body->read(65536);
                    if (ob_get_level() > 0) {
                        @ob_flush();
                    }
                    flush();
                }
            } catch (Throwable $e) {
                // Die Header sind schon raus: nichts mehr in den Strom schreiben, nur protokollieren.
                // Der Strom endet zu frueh; bei gesetzter Content-Length merkt der Browser das.
                Log::warning('PEB download aborted mid-stream.', ['backup' => $backup, 'error' => $e->getMessage()]);
            } finally {
                try {
                    $body->close();
                } catch (Throwable) {
                    // egal
                }
            }
        }, self::downloadName($path), $headers);
    }

    /** 204, wenn die Datei laut Index existiert (oder der Index unvollstaendig ist); sonst der Fehler des Gateways. */
    private function probe(string $backup, string $path, GatewayClient $gateway): JsonResponse
    {
        $dir = str_contains($path, '/') ? substr($path, 0, (int) strrpos($path, '/')) : '';
        $name = basename($path);

        for ($offset = 0; $offset < 20000; $offset += 1000) {
            $page = $gateway->get("backups/{$backup}/files", ['path' => $dir, 'limit' => 1000, 'offset' => $offset]);
            foreach ((array) ($page['items'] ?? []) as $item) {
                if (($item['name'] ?? null) === $name) {
                    if (($item['type'] ?? null) !== 'file') {
                        throw new GatewayException('not_a_file', 'This entry is not a regular file.', 400);
                    }

                    return (new JsonResponse(null, 204))->header('Cache-Control', 'no-store');
                }
            }
            if ($offset + 1000 >= (int) ($page['total'] ?? 0)) {
                break;
            }
        }

        // Nicht im Index: bei unvollstaendigem Index sucht das Gateway im Archiv, sonst gibt es sie nicht.
        if (! empty($page['index_truncated'])) {
            return (new JsonResponse(null, 204))->header('Cache-Control', 'no-store');
        }

        throw new GatewayException('not_found', 'The file is not part of this backup.', 404);
    }

    /** `path` roh aus dem Query-String lesen: TrimStrings wuerde Namen mit Leerzeichen am Rand veraendern. */
    private static function rawPath(Request $request): ?string
    {
        parse_str((string) $request->server->get('QUERY_STRING', ''), $query);
        $path = $query['path'] ?? null;

        return is_string($path) ? $path : null;
    }

    private function validatePath(string $path, bool $required): void
    {
        Validator::make(['path' => $path], ['path' => [$required ? 'required' : 'nullable', 'string', 'max:4096']])->validate();
    }

    /** Dateiname fuer Content-Disposition: ohne Pfad und Steuerzeichen, sonst "download". */
    public static function downloadName(string $path): string
    {
        $name = basename(str_replace('\\', '/', $path));
        $name = preg_replace('/[\x00-\x1F\x7F]+/u', '', $name) ?? '';
        $name = preg_replace('/[\x00-\x1F\x7F]+/', '', $name) ?? '';

        return trim($name) === '' ? 'download' : $name;
    }
}

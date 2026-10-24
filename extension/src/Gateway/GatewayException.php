<?php

namespace PebBackups\Gateway;

use Illuminate\Http\JsonResponse;
use RuntimeException;

/**
 * Fehler beim Zugriff auf das Gateway. Wird von Laravel selbst in JSON gerendert
 * ({"error": "<code>", "message": "..."}), nie als Stacktrace.
 *
 * Wichtig: 401/403 des Gateways werden NICHT durchgereicht (die Oberflaeche
 * koennte sie als abgelaufene Panel-Sitzung deuten), sondern als 502 gemeldet.
 */
class GatewayException extends RuntimeException
{
    public function __construct(
        public readonly string $errorCode,
        string $message,
        public readonly int $status = 502,
        public readonly ?int $retryAfter = null,
    ) {
        parent::__construct($message);
    }

    public function render(): JsonResponse
    {
        $response = new JsonResponse(['error' => $this->errorCode, 'message' => $this->getMessage()], $this->status);
        if ($this->retryAfter !== null) {
            $response->headers->set('Retry-After', (string) $this->retryAfter);
        }
        $response->headers->set('Cache-Control', 'no-store');

        return $response;
    }
}

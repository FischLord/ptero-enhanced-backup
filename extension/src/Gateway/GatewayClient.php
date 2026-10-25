<?php

namespace PebBackups\Gateway;

use Illuminate\Http\Client\ConnectionException;
use Illuminate\Http\Client\Factory;
use Illuminate\Http\Client\PendingRequest;
use Illuminate\Http\Client\Pool;
use Illuminate\Http\Client\Response;
use Throwable;

/** Duenner Client fuer die Admin-API des Gateways (docs/admin-api.md). */
class GatewayClient
{
    public function __construct(private readonly Factory $http, private readonly GatewayConfig $config) {}

    public function config(): GatewayConfig
    {
        return $this->config;
    }

    /**
     * @param  array<string, scalar|null>  $query
     * @return array<string, mixed>
     *
     * @throws GatewayException
     */
    public function get(string $path, array $query = []): array
    {
        return $this->decode($this->send('get', $path, $query, $this->request()));
    }

    /**
     * Mehrere GET-Aufrufe parallel. Gibt die dekodierten Antworten unter denselben Schluesseln zurueck.
     *
     * @param  array<string, array{0: string, 1?: array<string, scalar|null>}>  $calls  key => [path, query]
     * @return array<string, array<string, mixed>>
     *
     * @throws GatewayException der erste aufgetretene Fehler
     */
    public function getMany(array $calls): array
    {
        $this->request(); // prueft die Konfiguration

        $responses = $this->http->pool(function (Pool $pool) use ($calls) {
            $requests = [];
            foreach ($calls as $key => $call) {
                $requests[] = $pool->as((string) $key)
                    ->withToken($this->config->token)
                    ->acceptJson()
                    ->withoutRedirecting()
                    ->connectTimeout($this->config->connectTimeoutSeconds)
                    ->timeout($this->config->timeoutSeconds)
                    ->get($this->config->apiBase().'/'.ltrim($call[0], '/'), $call[1] ?? []);
            }

            return $requests;
        });

        $result = [];
        foreach ($calls as $key => $_) {
            $response = $responses[$key] ?? null;
            if ($response instanceof ConnectionException || ! $response instanceof Response) {
                $message = $response instanceof \Throwable ? $response->getMessage() : '';
                throw $this->connectionFailure($message);
            }
            $result[$key] = $response->successful() ? $this->decode($response) : throw $this->failure($response);
        }

        return $result;
    }

    /**
     * @param  array<string, mixed>  $body
     * @return array<string, mixed>
     *
     * @throws GatewayException
     */
    public function post(string $path, array $body): array
    {
        return $this->decode($this->send('post', $path, $body, $this->request()));
    }

    /**
     * @param  array<string, mixed>  $body
     * @return array<string, mixed>
     *
     * @throws GatewayException
     */
    public function put(string $path, array $body): array
    {
        return $this->decode($this->send('put', $path, $body, $this->request()));
    }

    /**
     * DELETE; die Antwort darf leer sein (204).
     *
     * @return array<string, mixed>
     *
     * @throws GatewayException
     */
    public function delete(string $path): array
    {
        $response = $this->send('delete', $path, [], $this->request());

        return trim($response->body()) === '' ? [] : $this->decode($response);
    }

    /**
     * Antwort ohne Puffer, fuer Einzeldatei-Downloads. Der Aufrufer liest den Body-Stream.
     *
     * @param  array<string, scalar|null>  $query
     *
     * @throws GatewayException
     */
    public function stream(string $path, array $query = []): Response
    {
        // PHP-Laufzeit der Anfrage nicht begrenzen: Der Download kann Minuten bis Stunden dauern.
        // (Webserver-/FPM-Zeitlimits muessen separat passen, siehe README.)
        @set_time_limit(0);

        // decode_content=false: Bytes unveraendert durchreichen, nichts im Panel entpacken.
        $request = $this->request()
            ->timeout($this->config->downloadTimeoutSeconds)
            ->withOptions(['stream' => true, 'decode_content' => false]);

        return $this->send('get', $path, $query, $request);
    }

    private function request(): PendingRequest
    {
        if (! $this->config->isConfigured()) {
            throw new GatewayException('not_configured', 'The gateway URL or admin token is not set. Configure them under Admin > Extensions > PEB Backups.', 503);
        }

        return $this->http
            ->withToken($this->config->token)
            ->acceptJson()
            ->withoutRedirecting()
            ->connectTimeout($this->config->connectTimeoutSeconds)
            ->timeout($this->config->timeoutSeconds);
    }

    /** @param array<string, mixed> $data */
    private function send(string $method, string $path, array $data, PendingRequest $request): Response
    {
        $url = $this->config->apiBase().'/'.ltrim($path, '/');

        try {
            $response = match ($method) {
                'post' => $request->asJson()->post($url, $data),
                'put' => $request->asJson()->put($url, $data),
                'delete' => $request->delete($url),
                default => $request->get($url, $data),
            };
        } catch (ConnectionException $e) {
            throw $this->connectionFailure($e->getMessage());
        } catch (Throwable $e) {
            throw new GatewayException('gateway_unreachable', 'The request to the gateway failed.', 502);
        }

        if ($response->successful()) {
            return $response;
        }

        throw $this->failure($response);
    }

    private function connectionFailure(string $message): GatewayException
    {
        $timeout = str_contains($message, 'cURL error 28') || stripos($message, 'timed out') !== false;

        return $timeout
            ? new GatewayException('gateway_timeout', 'The gateway did not answer in time.', 504)
            : new GatewayException('gateway_unreachable', 'The gateway is not reachable from the panel server.', 502);
    }

    private function failure(Response $response): GatewayException
    {
        $status = $response->status();
        $body = $response->json();
        $code = is_array($body) && is_string($body['error'] ?? null) ? $body['error'] : null;
        $message = is_array($body) && is_string($body['message'] ?? null) ? $body['message'] : null;

        // 404 ohne JSON-Fehlerkoerper: Das Gateway hat die Admin-API abgeschaltet (kein PEB_ADMIN_TOKEN).
        if ($status === 404 && $code === null) {
            return new GatewayException('api_disabled', 'The gateway admin API is disabled (PEB_ADMIN_TOKEN is not set on the gateway).', 502);
        }
        if ($status === 401 || $status === 403) {
            return new GatewayException('gateway_unauthorized', 'The gateway rejected the admin token. Check the token in the extension settings.', 502);
        }
        if (in_array($status, [400, 404, 409], true)) {
            return new GatewayException($code ?? 'gateway_error', $message ?? 'The gateway rejected the request.', $status);
        }
        if ($status === 503) {
            $retry = $response->header('Retry-After');

            return new GatewayException($code ?? 'busy', $message ?? 'The gateway is busy. Try again later.', 503, is_numeric($retry) ? (int) $retry : null);
        }

        return new GatewayException($code ?? 'gateway_error', $message ?? 'The gateway reported an error.', 502);
    }

    /** @return array<string, mixed> */
    private function decode(Response $response): array
    {
        $json = $response->json();
        if (! is_array($json)) {
            throw new GatewayException('gateway_invalid_response', 'The gateway answered with something that is not JSON.', 502);
        }

        return $json;
    }
}

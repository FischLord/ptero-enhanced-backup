<?php

use Illuminate\Http\Client\ConnectionException;
use Illuminate\Http\Client\Factory;
use Illuminate\Http\Client\Request;
use PebBackups\Gateway\GatewayClient;
use PebBackups\Gateway\GatewayConfig;
use PebBackups\Gateway\GatewayException;

function peb_client(Factory $http, ?GatewayConfig $config = null): GatewayClient
{
    return new GatewayClient($http, $config ?? new GatewayConfig('http://gw.test:9000', 'secret-token'));
}

function peb_error(callable $fn): GatewayException
{
    try {
        $fn();
    } catch (GatewayException $e) {
        return $e;
    }
    throw new RuntimeException('GatewayException expected');
}

test('sends the bearer token to /admin/v1 and returns decoded JSON', function () {
    $http = new Factory;
    $http->fake(['gw.test:9000/*' => Factory::response(['backups_count' => 3])]);

    expect(peb_client($http)->get('summary', ['days' => 30]))->toBe(['backups_count' => 3]);

    $http->assertSent(function (Request $request) {
        return str_starts_with($request->url(), 'http://gw.test:9000/admin/v1/summary')
            && $request->hasHeader('Authorization', 'Bearer secret-token');
    });
});

test('posts JSON bodies', function () {
    $http = new Factory;
    $http->fake(['*' => Factory::response(['id' => 15, 'job_ids' => [15]], 202)]);

    expect(peb_client($http)->post('maintenance/run', ['jobs' => ['check']]))->toBe(['id' => 15, 'job_ids' => [15]]);

    $http->assertSent(fn (Request $r) => $r->method() === 'POST' && $r->data() === ['jobs' => ['check']]);
});

test('reports a missing configuration without calling the gateway', function () {
    $http = new Factory;
    $http->fake();
    $e = peb_error(fn () => peb_client($http, new GatewayConfig('', ''))->get('status'));

    expect($e->errorCode)->toBe('not_configured')->and($e->status)->toBe(503);
    $http->assertNothingSent();
});

test('maps a refused connection to gateway_unreachable (502)', function () {
    $http = new Factory;
    $http->fake(fn () => throw new ConnectionException('cURL error 7: Failed to connect to gw.test port 9000'));
    $e = peb_error(fn () => peb_client($http)->get('status'));

    expect($e->errorCode)->toBe('gateway_unreachable')->and($e->status)->toBe(502);
});

test('maps a timeout to gateway_timeout (504)', function () {
    $http = new Factory;
    $http->fake(fn () => throw new ConnectionException('cURL error 28: Operation timed out after 15001 milliseconds'));
    $e = peb_error(fn () => peb_client($http)->get('status'));

    expect($e->errorCode)->toBe('gateway_timeout')->and($e->status)->toBe(504);
});

test('never passes 401 or 403 through as the panel session would be mistaken for expired', function (int $status) {
    $http = new Factory;
    $http->fake(['*' => Factory::response(['error' => 'unauthorized', 'message' => 'bad token'], $status)]);
    $e = peb_error(fn () => peb_client($http)->get('status'));

    expect($e->errorCode)->toBe('gateway_unauthorized')->and($e->status)->toBe(502);
})->with([401, 403]);

test('a 404 without JSON error body means the admin API is disabled', function () {
    $http = new Factory;
    $http->fake(['*' => Factory::response('404 page not found', 404)]);
    $e = peb_error(fn () => peb_client($http)->get('status'));

    expect($e->errorCode)->toBe('api_disabled')->and($e->status)->toBe(502);
});

test('keeps gateway errors for 400, 404 and 409 with their code and message', function (int $status, string $code) {
    $http = new Factory;
    $http->fake(['*' => Factory::response(['error' => $code, 'message' => 'details'], $status)]);
    $e = peb_error(fn () => peb_client($http)->get('backups/x/files'));

    expect($e->errorCode)->toBe($code)->and($e->status)->toBe($status)->and($e->getMessage())->toBe('details');
})->with([[400, 'not_a_file'], [404, 'not_found'], [409, 'busy'], [409, 'not_complete']]);

test('forwards 503 with Retry-After', function () {
    $http = new Factory;
    $http->fake(['*' => Factory::response(['error' => 'busy', 'message' => 'maintenance'], 503, ['Retry-After' => '120'])]);
    $e = peb_error(fn () => peb_client($http)->stream('backups/x/file', ['path' => 'a']));

    expect($e->status)->toBe(503)->and($e->retryAfter)->toBe(120);
    expect($e->render()->headers->get('Retry-After'))->toBe('120');
});

test('turns gateway 5xx and non-JSON bodies into a clean 502', function () {
    $http = new Factory;
    $http->fake(['*' => Factory::response(['error' => 'internal', 'message' => 'boom'], 500)]);
    $e = peb_error(fn () => peb_client($http)->get('status'));
    expect($e->errorCode)->toBe('internal')->and($e->status)->toBe(502);

    $http = new Factory;
    $http->fake(['*' => Factory::response('<html>oops</html>', 200)]);
    $e = peb_error(fn () => peb_client($http)->get('status'));
    expect($e->errorCode)->toBe('gateway_invalid_response');
});

test('renders the error as JSON that never contains the token', function () {
    $e = new GatewayException('gateway_unreachable', 'The gateway is not reachable from the panel server.', 502);
    $response = $e->render();

    expect($response->getStatusCode())->toBe(502)
        ->and(json_decode($response->getContent(), true))->toBe(['error' => 'gateway_unreachable', 'message' => 'The gateway is not reachable from the panel server.'])
        ->and($response->getContent())->not->toContain('secret-token');
});

test('runs several calls in parallel and returns them by key', function () {
    $http = new Factory;
    $http->fake([
        '*/status' => Factory::response(['version' => 'dev']),
        '*/summary' => Factory::response(['backups_count' => 3]),
    ]);

    $r = peb_client($http)->getMany(['status' => ['status'], 'summary' => ['summary']]);

    expect($r)->toBe(['status' => ['version' => 'dev'], 'summary' => ['backups_count' => 3]]);
    $http->assertSentCount(2);
});

test('a failing parallel call raises the mapped gateway error', function () {
    $http = new Factory;
    $http->fake([
        '*/status' => Factory::response(['version' => 'dev']),
        '*/summary' => Factory::response(['error' => 'unauthorized', 'message' => 'x'], 401),
    ]);

    $e = peb_error(fn () => peb_client($http)->getMany(['status' => ['status'], 'summary' => ['summary']]));

    expect($e->errorCode)->toBe('gateway_unauthorized');
});

test('redirects are not followed (a 3xx is an error, not a hop to another host)', function () {
    $http = new Factory;
    $http->fake(['*' => Factory::response('', 302, ['Location' => 'http://evil.test/'])]);
    $e = peb_error(fn () => peb_client($http)->get('status'));

    expect($e->status)->toBe(502);
});

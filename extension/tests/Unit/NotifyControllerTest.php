<?php

use Illuminate\Http\Client\Factory;
use Illuminate\Http\Request;
use PebBackups\Gateway\GatewayClient;
use PebBackups\Gateway\GatewayConfig;
use PebBackups\Gateway\GatewayException;
use PebBackups\Http\Controllers\NotifyController;

function notify_gateway(Factory $http): GatewayClient
{
    return new GatewayClient($http, new GatewayConfig('http://gw.test:9000', 'secret-token'));
}

test('passes the redacted channel list through without caching', function () {
    $body = ['lang' => 'en', 'channels' => [['type' => 'webhook', 'target' => 'https://discord.com', 'format' => 'discord']]];
    $http = new Factory;
    $http->fake(['*' => Factory::response($body, 200)]);

    $response = (new NotifyController)->index(notify_gateway($http));

    expect($response->getData(true))->toBe($body)
        ->and($response->headers->get('Cache-Control'))->toContain('no-store');
});

test('an older gateway without the notify routes becomes not_supported (404)', function (string $body, int $status) {
    $http = new Factory;
    $http->fake(['*' => Factory::response($body, $status)]);

    try {
        (new NotifyController)->index(notify_gateway($http));
    } catch (GatewayException $e) {
        expect($e->errorCode)->toBe('not_supported')->and($e->status)->toBe(404)->and($e->getMessage())->toContain('0.3');

        return;
    }
    throw new RuntimeException('GatewayException expected');
})->with([['404 page not found', 404], ['{"error":"not_found","message":"x"}', 404]]);

test('a 429 from the test endpoint stays a 429', function () {
    $http = new Factory;
    $http->fake(['*' => Factory::response(['error' => 'rate_limited', 'message' => 'wait'], 429)]);

    try {
        (new NotifyController)->test(Request::create('/'), notify_gateway($http));
    } catch (GatewayException $e) {
        expect($e->errorCode)->toBe('rate_limited')->and($e->status)->toBe(429);

        return;
    }
    throw new RuntimeException('GatewayException expected');
});

<?php

use Illuminate\Http\Client\Factory;
use Illuminate\Http\Client\Request;
use PebBackups\Gateway\GatewayClient;
use PebBackups\Gateway\GatewayConfig;
use PebBackups\Services\ServerReconciler;

function peb_reconciler(Factory $http, array $uuids): ServerReconciler
{
    $client = new GatewayClient($http, new GatewayConfig('http://gw.test:9000', 'secret-token'));

    return new ServerReconciler($client, fn () => $uuids);
}

test('sends the unique server UUIDs of the panel to the gateway', function () {
    $http = new Factory;
    $http->fake(['*' => Factory::response(['skipped' => null, 'marked_deleted' => [], 'restored' => [], 'trashed' => 0])]);

    $result = peb_reconciler($http, ['aaaaaaaa-0000-4000-8000-000000000001', 'bbbbbbbb-0000-4000-8000-000000000002', 'aaaaaaaa-0000-4000-8000-000000000001'])->reconcile();

    expect($result['trashed'])->toBe(0);
    $http->assertSent(fn (Request $r) => $r->method() === 'POST'
        && str_ends_with($r->url(), '/admin/v1/servers/reconcile')
        && $r->data() === ['server_uuids' => ['aaaaaaaa-0000-4000-8000-000000000001', 'bbbbbbbb-0000-4000-8000-000000000002']]);
});

test('never sends an empty server list', function () {
    $http = new Factory;
    $http->fake(['*' => Factory::response([])]);

    expect(peb_reconciler($http, [])->reconcile())->toBeNull();

    $http->assertNothingSent();
});

test('reports a deleted server with its lowercase UUID', function () {
    $http = new Factory;
    $http->fake(['*' => Factory::response(['trashed' => 3])]);

    peb_reconciler($http, [])->markDeleted('AAAAAAAA-0000-4000-8000-000000000001');

    $http->assertSent(fn (Request $r) => $r->method() === 'POST'
        && str_ends_with($r->url(), '/admin/v1/servers/aaaaaaaa-0000-4000-8000-000000000001/deleted'));
});

test('sends a DELETE for the trash and returns the decoded answer', function () {
    $http = new Factory;
    $http->fake(['*' => Factory::response(['purged' => true])]);
    $client = new GatewayClient($http, new GatewayConfig('http://gw.test:9000', 'secret-token'));

    expect($client->delete('trash/dc936ab6-2ac7-497a-8563-9626376dd2cb'))->toBe(['purged' => true]);

    $http->assertSent(fn (Request $r) => $r->method() === 'DELETE' && str_ends_with($r->url(), '/admin/v1/trash/dc936ab6-2ac7-497a-8563-9626376dd2cb'));
});

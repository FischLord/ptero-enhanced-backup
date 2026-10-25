<?php

use Illuminate\Http\Client\ConnectionException;
use Illuminate\Http\Client\Factory;
use PebBackups\Gateway\GatewayConfig;
use PebBackups\Setup\BundledEgg;
use PebBackups\Setup\GatewayProvisioner;
use PebBackups\Setup\PanelAdapter;
use PebBackups\Setup\PanelBridge;
use PebBackups\Setup\PreCheck;
use PebBackups\Setup\S3Prober;
use PebBackups\Setup\SecretGenerator;
use PebBackups\Setup\SelfTest;
use PebBackups\Setup\SetupException;
use PebBackups\Setup\SnippetBuilder;

const PEB_GOOD_FACTS = [
    'driver' => 's3', 'endpoint' => 'https://peb.example.com', 'path_style' => true, 'max_part_size' => 268435456,
    'bucket' => 'pterodactyl', 'key' => 'AKIAXXXX', 'secret' => 'sekret', 'gateway_url' => 'https://peb.example.com', 'admin_token' => 'tok',
];

function peb_status(array $result, string $id): string
{
    foreach ($result['checks'] as $check) {
        if ($check['id'] === $id) {
            return $check['status'];
        }
    }
    throw new RuntimeException("check $id missing");
}

// --- Vorabpruefung -----------------------------------------------------------------------------

test('a complete configuration is ready', function () {
    $r = PreCheck::evaluate(PEB_GOOD_FACTS);

    expect($r['ready'])->toBeTrue()
        ->and(array_unique(array_column($r['checks'], 'status')))->toBe(['ok']);
});

test('an empty panel configuration fails every check except the part size, which falls back to the default', function () {
    $r = PreCheck::evaluate(['driver' => 'wings', 'path_style' => false, 'max_part_size' => 5368709120]);

    expect($r['ready'])->toBeFalse()
        ->and(peb_status($r, 'driver'))->toBe('fail')
        ->and(peb_status($r, 'endpoint'))->toBe('fail')
        ->and(peb_status($r, 'path_style'))->toBe('fail')
        ->and(peb_status($r, 'credentials'))->toBe('fail')
        ->and(peb_status($r, 'gateway_url'))->toBe('fail')
        ->and(peb_status($r, 'admin_token'))->toBe('fail')
        ->and(peb_status($r, 'part_size'))->toBe('warn');
});

test('part sizes warn when very small or above the gateway limit', function (int $bytes, string $status) {
    expect(peb_status(PreCheck::evaluate(['max_part_size' => $bytes] + PEB_GOOD_FACTS), 'part_size'))->toBe($status);
})->with([
    'zero' => [0, 'fail'],
    '1 MiB' => [1048576, 'warn'],
    '16 MiB' => [16777216, 'ok'],
    '256 MiB' => [268435456, 'ok'],
    '1 GiB' => [1073741824, 'ok'],
    '5 GiB' => [5368709120, 'warn'],
]);

test('warnings alone do not block, one missing credential does', function () {
    expect(PreCheck::evaluate(['max_part_size' => 1048576] + PEB_GOOD_FACTS)['ready'])->toBeTrue()
        ->and(PreCheck::evaluate(['secret' => ''] + PEB_GOOD_FACTS)['ready'])->toBeFalse();
});

test('the pre-check output never contains secret values or addresses', function () {
    $json = json_encode(PreCheck::evaluate(PEB_GOOD_FACTS));

    expect($json)->not->toContain('AKIAXXXX')->not->toContain('sekret')->not->toContain('peb.example.com')->not->toContain('tok"');
});

// --- Geheimnisse -------------------------------------------------------------------------------

test('generated secrets have the right length and alphabet and differ every time', function () {
    expect(SecretGenerator::accessKey())->toMatch('/^[A-Z0-9]{20}$/')
        ->and(SecretGenerator::secretKey())->toMatch('/^[A-Za-z0-9]{40}$/')
        ->and(SecretGenerator::adminToken())->toMatch('/^[A-Za-z0-9]{48}$/')
        ->and(strlen(SecretGenerator::adminToken()))->toBeGreaterThanOrEqual(32)
        ->and(SecretGenerator::resticPassword())->toMatch('/^[A-Za-z0-9]{48}$/')
        ->and(SecretGenerator::secretKey())->not->toBe(SecretGenerator::secretKey());
});

test('the generator rejects an empty request and uses the whole alphabet', function () {
    expect(fn () => SecretGenerator::random(0))->toThrow(InvalidArgumentException::class);
    $seen = count_chars(SecretGenerator::random(4000, 'abc'), 3);
    expect($seen)->toBe('abc');
});

// --- Snippets ----------------------------------------------------------------------------------

test('the env block fills in values and keeps placeholders for missing ones', function () {
    $env = SnippetBuilder::env(['gateway_url' => 'https://peb.example.com', 'bucket' => 'bk', 'access_key' => 'AK', 'secret_key' => 'SK']);

    expect($env)->toBe(implode("\n", [
        'APP_BACKUP_DRIVER=s3',
        'AWS_ENDPOINT=https://peb.example.com',
        'AWS_USE_PATH_STYLE_ENDPOINT=true',
        'AWS_DEFAULT_REGION=us-east-1',
        'AWS_BACKUPS_BUCKET=bk',
        'AWS_ACCESS_KEY_ID=AK',
        'AWS_SECRET_ACCESS_KEY=SK',
        'BACKUP_MAX_PART_SIZE=268435456',
    ])."\n");

    $placeholders = SnippetBuilder::env([]);
    expect($placeholders)->toContain('AWS_ENDPOINT=<gateway address>')->toContain('AWS_BACKUPS_BUCKET=<bucket>')
        ->toContain('AWS_ACCESS_KEY_ID=<access key>')->toContain('AWS_SECRET_ACCESS_KEY=<secret>');
});

test('the wings snippet allows the gateway host', function () {
    expect(SnippetBuilder::wings('http://10.0.0.5:9000'))->toBe("system:\n  backups:\n    compression_level: none\n    restore_host_allowlist:\n      - 10.0.0.5\n")
        ->and(SnippetBuilder::wings(null))->toContain('<gateway host or IP>')
        ->and(SnippetBuilder::wings('not a url'))->toContain('<gateway host or IP>');
});

test('the gateway address comes from the allocation', function () {
    expect(SnippetBuilder::gatewayUrl('10.0.0.5', 9000))->toBe('http://10.0.0.5:9000')
        ->and(SnippetBuilder::gatewayUrl('fd00::1', 9000))->toBe('http://[fd00::1]:9000')
        ->and(SnippetBuilder::gatewayUrl('peb.example.com', 443, true))->toBe('https://peb.example.com:443');
});

// --- Mitgeliefertes Egg ------------------------------------------------------------------------

test('the bundled egg parses and has the variables the provisioner sets', function () {
    $egg = new BundledEgg;
    $defaults = $egg->defaults();

    expect($egg->name())->toBe('PEB Gateway')
        ->and($egg->author())->not->toBe('')
        ->and(array_keys($defaults))->toContain('PEB_ACCESS_KEY', 'PEB_SECRET_KEY', 'PEB_ADMIN_TOKEN', 'RESTIC_PASSWORD', 'PEB_BUCKET', 'PEB_REGION', 'RESTIC_REPOSITORY')
        ->and($defaults['PEB_BUCKET'])->toBe('pterodactyl');
});

test('the bundled egg has the structure the panel importer requires', function () {
    $data = (new BundledEgg)->data();

    expect($data['meta']['version'])->toBe('PTDL_v2')
        ->and($data['docker_images'])->not->toBeEmpty()
        ->and($data['scripts']['installation'])->toHaveKeys(['script', 'container', 'entrypoint'])
        ->and($data['config'])->toHaveKeys(['files', 'startup', 'logs', 'stop']);
    foreach ($data['variables'] as $variable) {
        expect($variable)->toHaveKeys(['name', 'description', 'env_variable', 'default_value', 'user_viewable', 'user_editable', 'rules']);
    }
});

test('a missing egg file gives a clean error', function () {
    expect(fn () => (new BundledEgg('/nonexistent/egg.json'))->json())->toThrow(SetupException::class);
});

// --- Adapter-Rueckfall -------------------------------------------------------------------------

test('without the panel classes the adapter reports everything as unavailable', function () {
    $adapter = new PanelAdapter(array_map(fn () => 'Missing\\Nothing', PanelAdapter::DEFAULT_CLASSES));

    expect($adapter->availability())->toMatchArray(['egg_import' => false, 'egg_update' => false, 'server_create' => false]);
});

test('a missing method on an existing class also counts as unavailable', function () {
    $adapter = new PanelAdapter(['egg_data' => stdClass::class]);

    expect($adapter->availability()['egg_import'])->toBeFalse();
});

test('the adapter finds the 2.0 panel classes', function () {
    if (! class_exists('Pterodactyl\\Data\\EggImportData')) {
        $this->markTestSkipped('panel classes missing');
    }
    $a = (new PanelAdapter)->availability();

    expect($a['egg_import'])->toBeTrue()->and($a['egg_update'])->toBeTrue()->and($a['server_create'])->toBeTrue();
});

// --- Gateway-Server anlegen (Fake) -------------------------------------------------------------

class FakePanel implements PanelBridge
{
    /** @var array<string, mixed>|null */
    public ?array $created = null;
    public bool $create = true;
    public ?array $egg = ['id' => 7, 'name' => 'PEB Gateway'];
    public bool $free = true;
    public ?string $alias = '10.0.0.5';
    public string $ip = '0.0.0.0';
    /** @var array<string, string>|null */
    public ?array $variables = null;

    public function availability(): array
    {
        return ['egg_import' => true, 'egg_update' => true, 'server_create' => $this->create, 's3_probe' => true];
    }

    public function backupFacts(): array
    {
        return [];
    }

    public function findEgg(string $name, string $author): ?array
    {
        return $this->egg;
    }

    public function importEgg(string $json): int
    {
        return 7;
    }

    public function updateEgg(int $eggId, string $json): void {}

    public function eggDetails(int $eggId): array
    {
        return ['id' => $eggId, 'image' => 'ghcr.io/x/peb-gateway:latest', 'startup' => '/usr/local/bin/peb-gateway', 'variables' => $this->variables ?? (new BundledEgg)->defaults()];
    }

    public function nodes(): array
    {
        return [];
    }

    public function freeAllocations(int $nodeId): array
    {
        return [];
    }

    public function allocation(int $allocationId): ?array
    {
        return ['id' => $allocationId, 'ip' => $this->ip, 'alias' => $this->alias, 'port' => 9000, 'free' => $this->free];
    }

    public function users(string $query): array
    {
        return [];
    }

    public function createServer(array $input): array
    {
        $this->created = $input;

        return ['id' => 12, 'uuid' => '11111111-2222-3333-4444-555555555555'];
    }
}

function peb_form(array $over = []): array
{
    return $over + ['name' => 'PEB Gateway', 'owner_id' => 1, 'allocation_id' => 5, 'memory' => 1024, 'disk' => 20480, 'bucket' => 'pterodactyl', 'region' => 'us-east-1', 'repository' => '/mnt/peb/repo'];
}

test('server creation generates the secrets, sets them as egg variables and returns them once', function () {
    $panel = new FakePanel;
    $r = (new GatewayProvisioner($panel, new BundledEgg))->provision(peb_form());

    $env = $panel->created['environment'];
    expect($env['PEB_ACCESS_KEY'])->toBe($r['credentials']['access_key'])
        ->and($env['PEB_SECRET_KEY'])->toBe($r['credentials']['secret_key'])
        ->and($env['PEB_ADMIN_TOKEN'])->toBe($r['credentials']['admin_token'])
        ->and($env['RESTIC_PASSWORD'])->toBe($r['credentials']['restic_password'])
        ->and(strlen($env['PEB_ADMIN_TOKEN']))->toBeGreaterThanOrEqual(32)
        ->and($env['RESTIC_REPOSITORY'])->toBe('/mnt/peb/repo')
        ->and($env['PEB_BUCKET'])->toBe('pterodactyl')
        ->and($env['TZ'])->toBe('Europe/Berlin') // untouched egg default
        ->and($panel->created)->toMatchArray(['egg_id' => 7, 'allocation_id' => 5, 'backup_limit' => 0, 'memory' => 1024, 'disk' => 20480, 'owner_id' => 1])
        ->and($r['gateway_url'])->toBe('http://10.0.0.5:9000')
        ->and($r['snippets']['env'])->toContain('AWS_ENDPOINT=http://10.0.0.5:9000')->toContain('AWS_ACCESS_KEY_ID='.$r['credentials']['access_key'])
        ->and($r['snippets']['wings'])->toContain('- 10.0.0.5')
        ->and($r['server'])->toBe(['id' => 12, 'uuid' => '11111111-2222-3333-4444-555555555555', 'name' => 'PEB Gateway']);
});

test('every creation produces fresh secrets and an empty repository keeps the egg default', function () {
    $panel = new FakePanel;
    $p = new GatewayProvisioner($panel, new BundledEgg);
    $a = $p->provision(peb_form(['repository' => '']));
    $b = $p->provision(peb_form());

    expect($a['credentials']['restic_password'])->not->toBe($b['credentials']['restic_password']);
    $p->provision(peb_form(['repository' => null]));
    expect($panel->created['environment']['RESTIC_REPOSITORY'])->toBe('');
});

test('server creation refuses when the panel cannot do it, the egg is missing or the allocation is taken', function (callable $arrange, string $code) {
    $panel = new FakePanel;
    $arrange($panel);

    try {
        (new GatewayProvisioner($panel, new BundledEgg))->provision(peb_form());
        $this->fail('expected a SetupException');
    } catch (SetupException $e) {
        expect($e->errorCode)->toBe($code)->and($panel->created)->toBeNull();
    }
})->with([
    'not available' => [fn ($p) => $p->create = false, 'not_available'],
    'no egg' => [fn ($p) => $p->egg = null, 'egg_missing'],
    'taken' => [fn ($p) => $p->free = false, 'allocation_unavailable'],
]);

test('an outdated egg without one of the secret variables aborts before anything is created', function () {
    $panel = new FakePanel;
    $vars = (new BundledEgg)->defaults();
    unset($vars['RESTIC_PASSWORD']);
    $panel->variables = $vars;

    try {
        (new GatewayProvisioner($panel, new BundledEgg))->provision(peb_form());
        $this->fail('expected a SetupException');
    } catch (SetupException $e) {
        expect($e->errorCode)->toBe('egg_outdated')->and($e->status)->toBe(409)->and($panel->created)->toBeNull();
    }
});

test('the gateway address is the IP alias, else the form host, and never 0.0.0.0 or ::', function (?string $alias, ?string $host, ?string $expected) {
    $panel = new FakePanel;
    $panel->alias = $alias;
    $p = new GatewayProvisioner($panel, new BundledEgg);

    if ($expected === null) {
        expect(fn () => $p->provision(peb_form(['host' => $host])))->toThrow(SetupException::class);
        expect($panel->created)->toBeNull();

        return;
    }
    $r = $p->provision(peb_form(['host' => $host]));
    expect($r['gateway_url'])->toBe('http://'.$expected.':9000')
        ->and($r['snippets']['env'])->not->toContain('0.0.0.0')
        ->and($r['snippets']['wings'])->toContain('- '.$expected);
})->with([
    'alias wins' => ['10.0.0.5', 'other.example.com', '10.0.0.5'],
    'form host' => [null, 'gw.example.com', 'gw.example.com'],
    'no host' => [null, null, null],
    'blank host' => [null, '  ', null],
    'any address v4' => [null, '0.0.0.0', null],
    'any address v6' => [null, '::', null],
    'alias is any address' => ['0.0.0.0', '', null],
]);

test('egg update and server creation do not depend on egg import', function () {
    $c = array_map(fn () => 'Missing\\Nothing', PanelAdapter::DEFAULT_CLASSES);
    if (! class_exists('Pterodactyl\\Models\\Egg')) {
        $this->markTestSkipped('panel classes missing');
    }
    $a = (new PanelAdapter(['egg_data' => 'Missing\\Nothing', 'egg_importer' => 'Missing\\Nothing']))->availability();

    expect($a['egg_import'])->toBeFalse()->and($a['egg_update'])->toBeTrue()->and($a['server_create'])->toBeTrue();
});

// --- Selbsttest --------------------------------------------------------------------------------

class FakeS3 implements S3Prober
{
    public array $calls = [];

    public function __construct(private readonly ?SetupException $createError = null, private readonly ?SetupException $abortError = null) {}

    public function create(string $key): string
    {
        $this->calls[] = ['create', $key];
        if ($this->createError) {
            throw $this->createError;
        }

        return 'upload-1';
    }

    public function abort(string $key, string $uploadId): void
    {
        $this->calls[] = ['abort', $key, $uploadId];
        if ($this->abortError) {
            throw $this->abortError;
        }
    }
}

function peb_selftest(Factory $http, ?FakeS3 $s3 = null, string $url = 'https://gw.test', string $token = 'tok', ?string $endpoint = 'https://gw.test'): array
{
    return (new SelfTest($http, new GatewayConfig($url, $token), $s3 ?? new FakeS3, $endpoint))->run();
}

function peb_hint(array $r, string $id): ?string
{
    foreach ($r['checks'] as $c) {
        if ($c['id'] === $id) {
            return $c['hint'];
        }
    }

    return null;
}

test('self-test passes against a healthy gateway', function () {
    $http = new Factory;
    $http->fake(['gw.test/healthz' => Factory::response("ok\n"), 'gw.test/admin/v1/status' => Factory::response(['version' => 'dev'])]);
    $s3 = new FakeS3;
    $r = peb_selftest($http, $s3);

    expect($r['ok'])->toBeTrue()
        ->and(array_column($r['checks'], 'status', 'id'))->toBe(['gateway' => 'ok', 'token' => 'ok', 's3' => 'ok', 'https' => 'ok'])
        ->and($s3->calls[0][0])->toBe('create')
        ->and($s3->calls[0][1])->toMatch('#^[0-9a-f-]{36}/[0-9a-f-]{36}\.tar\.gz$#')
        ->and($s3->calls[1])->toBe(['abort', $s3->calls[0][1], 'upload-1']);
});

test('an unreachable gateway is a clean failure, not an exception, and the token check does not wait again', function () {
    $calls = 0;
    $http = new Factory;
    $http->fake(function () use (&$calls) {
        $calls++;
        throw new ConnectionException('cURL error 7: Failed to connect');
    });
    $r = peb_selftest($http);

    expect($r['ok'])->toBeFalse()
        ->and(peb_hint($r, 'gateway'))->toBe('gateway_unreachable')
        ->and(peb_hint($r, 'token'))->toBe('gateway_first');
    expect($calls)->toBe(1);
});

test('a timeout and a foreign server on the address are told apart', function () {
    $timeout = new Factory;
    $timeout->fake(fn () => throw new ConnectionException('cURL error 28: Operation timed out'));
    $foreign = new Factory;
    $foreign->fake(['*' => Factory::response('<html>nginx</html>', 200)]);

    expect(peb_hint(peb_selftest($timeout), 'gateway'))->toBe('gateway_timeout')
        ->and(peb_hint(peb_selftest($foreign), 'gateway'))->toBe('gateway_not_peb');
});

test('token problems are reported precisely', function (int $status, string $hint) {
    $http = new Factory;
    $http->fake(['gw.test/healthz' => Factory::response('ok'), 'gw.test/admin/v1/status' => Factory::response($status === 404 ? '' : ['error' => 'unauthorized'], $status)]);

    expect(peb_hint(peb_selftest($http), 'token'))->toBe($hint);
})->with([[401, 'token_rejected'], [404, 'api_disabled']]);

test('missing settings fail with a hint instead of a request', function () {
    $http = new Factory;
    $http->fake();
    $r = peb_selftest($http, null, '', '', null);

    expect(peb_hint($r, 'gateway'))->toBe('gateway_url_missing')
        ->and(peb_hint($r, 'token'))->toBe('admin_token_missing')
        ->and(peb_hint($r, 'https'))->toBe('gateway_url_missing');
    $http->assertNothingSent();
});

test('s3 failures carry the code of the prober and no abort is attempted', function () {
    $http = new Factory;
    $http->fake(['gw.test/healthz' => Factory::response('ok'), 'gw.test/admin/v1/status' => Factory::response(['version' => 'dev'])]);
    $s3 = new FakeS3(new SetupException('s3_denied'));
    $r = peb_selftest($http, $s3);

    expect(peb_hint($r, 's3'))->toBe('s3_denied')->and($s3->calls)->toHaveCount(1);
});

test('a failed abort is only a warning', function () {
    $http = new Factory;
    $http->fake(['gw.test/healthz' => Factory::response('ok'), 'gw.test/admin/v1/status' => Factory::response(['version' => 'dev'])]);
    $r = peb_selftest($http, new FakeS3(null, new SetupException('s3_error')));

    expect(array_column($r['checks'], 'status', 'id')['s3'])->toBe('warn')->and($r['ok'])->toBeTrue();
});

test('plain http gives a warning that does not fail the test', function () {
    $http = new Factory;
    $http->fake(['gw.test/healthz' => Factory::response('ok'), 'gw.test/admin/v1/status' => Factory::response(['version' => 'dev'])]);
    $r = peb_selftest($http, null, 'http://gw.test', 'tok', 'https://gw.test');

    expect(array_column($r['checks'], 'status', 'id')['https'])->toBe('warn')
        ->and(peb_hint($r, 'https'))->toBe('not_https');
});

// --- Texte -------------------------------------------------------------------------------------

test('every hint code the server can emit has an English and a German text', function () {
    $dir = dirname(__DIR__, 2).'/src/client/lang';
    $en = json_decode(file_get_contents("$dir/en.json"), true);
    $de = json_decode(file_get_contents("$dir/de.json"), true);
    $hints = [
        'driver_not_s3', 'endpoint_missing', 'path_style_off', 'credentials_missing', 'part_size_invalid', 'part_size_small', 'part_size_large',
        'gateway_url_missing', 'admin_token_missing', 'gateway_unreachable', 'gateway_timeout', 'gateway_not_peb', 'gateway_first', 'token_rejected',
        'api_disabled', 'no_s3_disk', 's3_denied', 's3_unreachable', 's3_timeout', 's3_error', 's3_abort_failed', 'not_https',
    ];
    foreach ($hints as $hint) {
        expect($en)->toHaveKey("setup_fix_$hint");
        expect($de)->toHaveKey("setup_fix_$hint");
    }
});

test('no translation contains a Laravel style placeholder', function () {
    $dir = dirname(__DIR__, 2).'/src/client/lang';
    foreach (['en', 'de'] as $locale) {
        foreach (json_decode(file_get_contents("$dir/$locale.json"), true) as $key => $text) {
            expect(preg_match('/(?<![\w{]):[A-Za-z_]\w*/', $text))->toBe(0, "$locale.$key contains a :word placeholder");
        }
    }
});

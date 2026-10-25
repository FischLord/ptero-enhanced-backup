<?php

namespace PebBackups\Setup;

use Illuminate\Http\UploadedFile;
use Illuminate\Support\Facades\Log;
use Illuminate\Validation\ValidationException;
use Throwable;

/**
 * Einzige Stelle, die interne Klassen des Panels fuer Egg-Import und Serveranlage anfasst.
 * Fehlen Klassen oder Methoden (andere Panel-Version), meldet availability() das, und der
 * Assistent zeigt eine Anleitung fuer den manuellen Weg statt zu scheitern.
 *
 * Die Klassennamen sind ueber den Konstruktor austauschbar (Test des Rueckfalls).
 */
class PanelAdapter implements PanelBridge
{
    public const DEFAULT_CLASSES = [
        'egg_data' => 'Pterodactyl\\Data\\EggImportData',
        'egg_importer' => 'Pterodactyl\\Contracts\\Eggs\\ImportsEggs',
        'egg_updater' => 'Pterodactyl\\Contracts\\Eggs\\UpdatesEggsFromImports',
        'server_creator' => 'Pterodactyl\\Contracts\\Servers\\CreatesServers',
        'egg' => 'Pterodactyl\\Models\\Egg',
        'node' => 'Pterodactyl\\Models\\Node',
        'allocation' => 'Pterodactyl\\Models\\Allocation',
        'user' => 'Pterodactyl\\Models\\User',
        'daemon_error' => 'Pterodactyl\\Exceptions\\Http\\Connection\\DaemonConnectionException',
    ];

    /** @var array<string, string> */
    private array $classes;

    /** @param array<string, string> $classes */
    public function __construct(array $classes = [])
    {
        $this->classes = $classes + self::DEFAULT_CLASSES;
    }

    public function availability(): array
    {
        $c = $this->classes;
        $models = class_exists($c['egg']) && class_exists($c['node']) && class_exists($c['allocation']) && class_exists($c['user']);
        $import = $models && class_exists($c['egg_data']) && method_exists($c['egg_data'], 'fromJson')
            && interface_exists($c['egg_importer']) && method_exists($c['egg_importer'], 'importParsed');
        $update = $models && interface_exists($c['egg_updater']) && method_exists($c['egg_updater'], 'update');
        $create = $models && interface_exists($c['server_creator']) && method_exists($c['server_creator'], 'create');

        return [
            'egg_import' => $import,
            'egg_update' => $update,
            'server_create' => $create,
            's3_probe' => PanelS3Prober::supported(),
        ];
    }

    public function backupFacts(): array
    {
        $disk = (array) config('backups.disks.s3', []);
        $string = fn ($v): ?string => is_string($v) && $v !== '' ? $v : null;

        return [
            'driver' => $string(config('backups.default')),
            'endpoint' => $string($disk['endpoint'] ?? null),
            'path_style' => (bool) ($disk['use_path_style_endpoint'] ?? false),
            'max_part_size' => (int) config('backups.max_part_size', 0),
            'bucket' => $string($disk['bucket'] ?? null),
            'key' => $string($disk['key'] ?? null),
            'secret' => $string($disk['secret'] ?? null),
            'region' => $string($disk['region'] ?? null),
        ];
    }

    public function findEgg(string $name, string $author): ?array
    {
        $egg = ($this->classes['egg'])::query()->where('name', $name)->where('author', $author)->orderBy('id')->first();

        return $egg ? ['id' => (int) $egg->id, 'name' => (string) $egg->name] : null;
    }

    public function importEgg(string $json): int
    {
        $c = $this->classes;
        $egg = app($c['egg_importer'])->importParsed(($c['egg_data'])::fromJson($json));

        return (int) $egg->id;
    }

    public function updateEgg(int $eggId, string $json): void
    {
        $c = $this->classes;
        $egg = ($c['egg'])::query()->findOrFail($eggId);

        $path = tempnam(sys_get_temp_dir(), 'peb-egg');
        try {
            file_put_contents($path, $json);
            // Test-Modus: Die Datei kommt nicht aus einem HTTP-Upload.
            app($c['egg_updater'])->update($egg, new UploadedFile($path, 'egg-peb-gateway.json', 'application/json', null, true));
        } finally {
            @unlink($path);
        }
    }

    public function eggDetails(int $eggId): array
    {
        $egg = ($this->classes['egg'])::query()->with('variables')->findOrFail($eggId);
        $images = (array) $egg->docker_images;
        $variables = [];
        foreach ($egg->variables as $variable) {
            $variables[(string) $variable->env_variable] = (string) $variable->default_value;
        }

        return ['id' => (int) $egg->id, 'image' => (string) (reset($images) ?: ''), 'startup' => (string) $egg->startup, 'variables' => $variables];
    }

    public function nodes(): array
    {
        $nodes = ($this->classes['node'])::query()
            ->withCount(['allocations as free_allocations' => fn ($q) => $q->whereNull('server_id')])
            ->orderBy('name')
            ->get(['id', 'name', 'fqdn']);

        return $nodes->map(fn ($n) => [
            'id' => (int) $n->id,
            'name' => (string) $n->name,
            'fqdn' => (string) $n->fqdn,
            'free_allocations' => (int) $n->getAttribute('free_allocations'),
        ])->all();
    }

    public function freeAllocations(int $nodeId): array
    {
        $rows = ($this->classes['allocation'])::query()
            ->where('node_id', $nodeId)->whereNull('server_id')
            ->orderBy('ip')->orderBy('port')->limit(500)
            ->get(['id', 'ip', 'ip_alias', 'port']);

        return $rows->map(fn ($a) => [
            'id' => (int) $a->id,
            'ip' => (string) $a->ip,
            'alias' => $a->ip_alias ?: null,
            'port' => (int) $a->port,
        ])->all();
    }

    public function allocation(int $allocationId): ?array
    {
        $a = ($this->classes['allocation'])::query()->find($allocationId, ['id', 'ip', 'ip_alias', 'port', 'server_id']);

        return $a ? ['id' => (int) $a->id, 'ip' => (string) $a->ip, 'alias' => $a->ip_alias ?: null, 'port' => (int) $a->port, 'free' => $a->server_id === null] : null;
    }

    public function users(string $query): array
    {
        // Nur Root-Admins: Besitzer und Unterbenutzer mit Startup-Recht sehen die Egg-Variablen, also auch die Geheimnisse.
        $q = ($this->classes['user'])::query()->where('root_admin', true)->orderBy('username')->limit(20);
        if ($query !== '') {
            $like = '%'.addcslashes($query, '%_\\').'%';
            $q->where(fn ($w) => $w->where('username', 'like', $like)->orWhere('email', 'like', $like));
        }

        return $q->get(['id', 'username', 'email'])->map(fn ($u) => [
            'id' => (int) $u->id,
            'username' => (string) $u->username,
            'email' => (string) $u->email,
        ])->all();
    }

    public function createServer(array $input): array
    {
        $c = $this->classes;

        try {
            $server = app($c['server_creator'])->create($input);
        } catch (ValidationException $e) {
            throw $e;
        } catch (Throwable $e) {
            // Bewusst weder Meldung noch Eingabe weiterreichen: Die Eingabe enthaelt die Geheimnisse. Geloggt wird nur die Klasse.
            Log::warning('PEB gateway server creation failed.', ['exception' => $e::class]);
            if (class_exists($c['daemon_error']) && $e instanceof $c['daemon_error']) {
                throw new SetupException('wings_unreachable', 'The panel could not reach Wings; the server was not created.', 502);
            }
            throw new SetupException('server_failed', 'The server could not be created.', 500);
        }

        return ['id' => (int) $server->id, 'uuid' => (string) $server->uuid];
    }
}

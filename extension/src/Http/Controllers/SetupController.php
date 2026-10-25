<?php

namespace PebBackups\Http\Controllers;

use Illuminate\Http\Client\Factory as HttpFactory;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Support\Facades\Log;
use Illuminate\Validation\ValidationException;
use PebBackups\Gateway\GatewayConfig;
use PebBackups\Setup\BundledEgg;
use PebBackups\Setup\GatewayProvisioner;
use PebBackups\Setup\PanelBridge;
use PebBackups\Setup\PreCheck;
use PebBackups\Setup\S3Prober;
use PebBackups\Setup\SelfTest;
use PebBackups\Setup\SetupException;
use PebBackups\Setup\SnippetBuilder;
use Throwable;

/**
 * Einrichtungsassistent. Antworten sind nie zwischenspeicherbar; Geheimnisse des Panels erscheinen nie,
 * die neu erzeugten Zugangsdaten nur in der Antwort von POST /setup/server.
 */
class SetupController
{
    public function __construct(private readonly PanelBridge $panel, private readonly BundledEgg $egg) {}

    private static function json(array $data, int $status = 200): JsonResponse
    {
        return (new JsonResponse($data, $status))->header('Cache-Control', 'no-store');
    }

    /** Vorabpruefung, Verfuegbarkeit, Egg-Stand und Vorlagen-Snippets. */
    public function index(GatewayConfig $config): JsonResponse
    {
        $facts = $this->panel->backupFacts();
        $pre = PreCheck::evaluate($facts + ['gateway_url' => $config->baseUrl, 'admin_token' => $config->token]);
        $available = $this->panel->availability();

        $egg = ['name' => null, 'exists' => false, 'id' => null, 'error' => null];
        $defaults = [];
        try {
            $egg['name'] = $this->egg->name();
            $defaults = $this->egg->defaults();
            if ($available['egg_import'] || $available['egg_update'] || $available['server_create']) {
                $found = $this->panel->findEgg($this->egg->name(), $this->egg->author());
                $egg['exists'] = $found !== null;
                $egg['id'] = $found['id'] ?? null;
            }
        } catch (SetupException $e) {
            $egg['error'] = $e->errorCode;
        } catch (Throwable) {
            $egg['error'] = 'egg_lookup_failed';
        }

        return self::json([
            'available' => $available,
            'ready' => $pre['ready'],
            'checks' => $pre['checks'],
            'egg' => $egg,
            'defaults' => [
                'name' => 'PEB Gateway',
                'bucket' => $defaults['PEB_BUCKET'] ?? 'pterodactyl',
                'region' => $defaults['PEB_REGION'] ?? 'us-east-1',
                'repository' => $defaults['RESTIC_REPOSITORY'] ?? '',
                'memory' => 1024,
                'disk' => 20480,
            ],
            'snippets' => [
                'env' => SnippetBuilder::env([
                    'gateway_url' => $config->baseUrl ?: null,
                    'region' => $facts['region'] ?? null,
                    'bucket' => $facts['bucket'] ?? null,
                ]),
                'wings' => SnippetBuilder::wings($config->baseUrl ?: null),
            ],
        ]);
    }

    /** Importiert das mitgelieferte Egg oder aktualisiert das vorhandene (kein Duplikat). */
    public function egg(Request $request): JsonResponse
    {
        $data = $request->validate(['mode' => ['required', 'in:import,update']]);
        $available = $this->panel->availability();
        $feature = $data['mode'] === 'import' ? 'egg_import' : 'egg_update';
        if (! $available[$feature]) {
            return self::json(['error' => 'not_available', 'message' => 'Egg import is not available in this panel version.'], 501);
        }

        try {
            $existing = $this->panel->findEgg($this->egg->name(), $this->egg->author());
            if ($data['mode'] === 'import') {
                if ($existing !== null) {
                    return self::json(['error' => 'egg_exists', 'message' => 'The egg already exists. Update it instead.', 'egg_id' => $existing['id']], 409);
                }
                $id = $this->panel->importEgg($this->egg->json());
            } else {
                if ($existing === null) {
                    return self::json(['error' => 'egg_missing', 'message' => 'The egg does not exist yet. Import it first.'], 409);
                }
                $this->panel->updateEgg($existing['id'], $this->egg->json());
                $id = $existing['id'];
            }
        } catch (SetupException $e) {
            return self::json(['error' => $e->errorCode, 'message' => $e->getMessage()], $e->status);
        } catch (Throwable $e) {
            Log::warning('PEB egg import failed.', ['exception' => $e::class]);

            return self::json(['error' => 'egg_failed', 'message' => 'The egg could not be imported. Check the panel log.'], 500);
        }

        return self::json(['egg_id' => $id, 'action' => $data['mode']], $data['mode'] === 'import' ? 201 : 200);
    }

    /** @param callable(): array<int, mixed> $load */
    private static function lookup(callable $load): JsonResponse
    {
        try {
            return self::json(['items' => $load()]);
        } catch (Throwable $e) {
            Log::warning('PEB setup lookup failed.', ['exception' => $e::class]);

            return self::json(['error' => 'setup_lookup_failed', 'message' => 'The list could not be loaded. Check the panel log.'], 500);
        }
    }

    public function nodes(): JsonResponse
    {
        return self::lookup(fn () => $this->panel->nodes());
    }

    public function allocations(int $node): JsonResponse
    {
        return self::lookup(fn () => $this->panel->freeAllocations($node));
    }

    public function users(Request $request): JsonResponse
    {
        $data = $request->validate(['q' => ['sometimes', 'nullable', 'string', 'max:100']]);

        return self::lookup(fn () => $this->panel->users((string) ($data['q'] ?? '')));
    }

    /** Legt den Gateway-Server an. Die Antwort zeigt die erzeugten Zugangsdaten genau einmal. */
    public function server(Request $request, GatewayProvisioner $provisioner): JsonResponse
    {
        $data = $request->validate([
            'name' => ['required', 'string', 'min:1', 'max:191'],
            'owner_id' => ['required', 'integer', 'exists:users,id,root_admin,1'],
            'allocation_id' => ['required', 'integer', 'exists:allocations,id'],
            'host' => ['sometimes', 'nullable', 'string', 'max:255', 'regex:/^[A-Za-z0-9.\-:\[\]]+$/'],
            'memory' => ['required', 'integer', 'min:256', 'max:1048576'],
            'disk' => ['required', 'integer', 'min:1024', 'max:104857600'],
            'bucket' => ['required', 'string', 'min:3', 'max:63', 'regex:/^[a-z0-9][a-z0-9.\-]*$/', 'not_in:admin'],
            'region' => ['required', 'string', 'max:32', 'regex:/^[A-Za-z0-9\-]+$/'],
            'repository' => ['sometimes', 'nullable', 'string', 'max:255'],
        ]);

        try {
            $result = $provisioner->provision($data);
        } catch (SetupException $e) {
            return self::json(['error' => $e->errorCode, 'message' => $e->getMessage()], $e->status);
        } catch (ValidationException $e) {
            throw $e;
        } catch (Throwable $e) {
            // Nur die Klasse loggen: Eingabe und Meldung koennen Geheimnisse enthalten.
            Log::warning('PEB gateway server creation failed.', ['exception' => $e::class]);

            return self::json(['error' => 'server_failed', 'message' => 'The server could not be created.'], 500);
        }

        Log::info('PEB gateway server created from the setup assistant.', ['user_id' => $request->user()?->id, 'server_id' => $result['server']['id']]);

        return self::json($result, 201);
    }

    public function selfTest(GatewayConfig $config, HttpFactory $http, S3Prober $s3): JsonResponse
    {
        $endpoint = $this->panel->backupFacts()['endpoint'] ?? null;

        return self::json((new SelfTest($http, $config, $s3, $endpoint))->run());
    }
}

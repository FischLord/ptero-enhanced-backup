<?php

namespace PebBackups\Setup;

/**
 * Legt den Gateway-Server an: erzeugt die Geheimnisse serverseitig, setzt sie als Egg-Variablen und
 * ruft den Serveranlage-Dienst des Panels. Die Geheimnisse stehen nur in der Rueckgabe (einmalige
 * Anzeige), werden nicht geloggt und nicht gespeichert.
 */
final class GatewayProvisioner
{
    public function __construct(private readonly PanelBridge $panel, private readonly BundledEgg $egg) {}

    /**
     * @param  array{name: string, owner_id: int, allocation_id: int, memory: int, disk: int, bucket: string, region: string, repository?: ?string, host?: ?string}  $form
     * @return array{server: array{id: int, uuid: string, name: string}, gateway_url: string, credentials: array<string, string>, snippets: array{env: string, wings: string}}
     *
     * @throws SetupException
     */
    public function provision(array $form): array
    {
        if (! ($this->panel->availability()['server_create'] ?? false)) {
            throw new SetupException('not_available', 'Server creation is not available in this panel version.', 501);
        }

        $egg = $this->panel->findEgg($this->egg->name(), $this->egg->author());
        if ($egg === null) {
            throw new SetupException('egg_missing', 'Import the egg first.', 409);
        }

        $allocation = $this->panel->allocation($form['allocation_id']);
        if ($allocation === null || ! $allocation['free']) {
            throw new SetupException('allocation_unavailable', 'The allocation does not exist or is already in use.', 422);
        }

        // Gateway-Adresse: IP-Alias der Allocation, sonst das Feld aus dem Formular. 0.0.0.0 und :: sind keine Adressen.
        $host = $allocation['alias'] ?? null;
        if ($host === null || $host === '') {
            $host = trim((string) ($form['host'] ?? ''));
        }
        if ($host === '' || in_array(trim($host, '[]'), ['0.0.0.0', '::', '0:0:0:0:0:0:0:0'], true)) {
            throw new SetupException('host_required', 'Enter the address under which the gateway can be reached.', 422);
        }

        $secrets = [
            'access_key' => SecretGenerator::accessKey(),
            'secret_key' => SecretGenerator::secretKey(),
            'admin_token' => SecretGenerator::adminToken(),
            'restic_password' => SecretGenerator::resticPassword(),
        ];

        $details = $this->panel->eggDetails($egg['id']);
        $environment = $details['variables'];
        $overrides = [
            'PEB_ACCESS_KEY' => $secrets['access_key'],
            'PEB_SECRET_KEY' => $secrets['secret_key'],
            'PEB_ADMIN_TOKEN' => $secrets['admin_token'],
            'RESTIC_PASSWORD' => $secrets['restic_password'],
            'PEB_BUCKET' => $form['bucket'],
            'PEB_REGION' => $form['region'],
        ];
        if (($form['repository'] ?? '') !== '' && $form['repository'] !== null) {
            $overrides['RESTIC_REPOSITORY'] = $form['repository'];
        }
        // Fehlt eine Variable im Egg des Panels (aelteres Egg), wuerde ein Geheimnis still nicht gesetzt, die Oberflaeche zeigte es aber an.
        foreach (array_keys($overrides) as $name) {
            if (! array_key_exists($name, $environment)) {
                throw new SetupException('egg_outdated', 'The egg in the panel lacks the variable '.$name.'. Update the egg first.', 409);
            }
        }
        foreach ($overrides as $name => $value) {
            $environment[$name] = $value;
        }

        $server = $this->panel->createServer([
            'name' => $form['name'],
            'description' => 'PEB backup gateway',
            'owner_id' => $form['owner_id'],
            'egg_id' => $egg['id'],
            'image' => $details['image'],
            'startup' => $details['startup'],
            'environment' => $environment,
            'memory' => $form['memory'],
            'swap' => 0,
            'disk' => $form['disk'],
            'io' => 500,
            'cpu' => 0,
            'allocation_id' => $allocation['id'],
            // Der Gateway-Server sichert sich nicht selbst.
            'backup_limit' => 0,
            'database_limit' => 0,
            'allocation_limit' => 0,
            'start_on_completion' => true,
        ]);

        $gatewayUrl = SnippetBuilder::gatewayUrl($host, $allocation['port']);

        return [
            'server' => ['id' => $server['id'], 'uuid' => $server['uuid'], 'name' => $form['name']],
            'gateway_url' => $gatewayUrl,
            'credentials' => $secrets + ['bucket' => $form['bucket'], 'region' => $form['region']],
            'snippets' => [
                'env' => SnippetBuilder::env([
                    'gateway_url' => $gatewayUrl,
                    'region' => $form['region'],
                    'bucket' => $form['bucket'],
                    'access_key' => $secrets['access_key'],
                    'secret_key' => $secrets['secret_key'],
                ]),
                'wings' => SnippetBuilder::wings($gatewayUrl),
            ],
        ];
    }
}

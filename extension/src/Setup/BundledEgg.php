<?php

namespace PebBackups\Setup;

/** Das mit der Extension ausgelieferte Egg (Kopie von egg/egg-peb-gateway.json, per `make egg-sync` aktuell gehalten). */
final class BundledEgg
{
    public function __construct(private readonly ?string $path = null) {}

    public function path(): string
    {
        return $this->path ?? dirname(__DIR__, 2).'/resources/egg/egg-peb-gateway.json';
    }

    /** @throws SetupException */
    public function json(): string
    {
        $json = is_file($this->path()) ? file_get_contents($this->path()) : false;
        if (! is_string($json) || $json === '') {
            throw new SetupException('egg_file_missing', 'The bundled egg file is missing.', 500);
        }

        return $json;
    }

    /** @return array<string, mixed> */
    public function data(): array
    {
        $data = json_decode($this->json(), true);
        if (! is_array($data)) {
            throw new SetupException('egg_file_invalid', 'The bundled egg file is not valid JSON.', 500);
        }

        return $data;
    }

    public function name(): string
    {
        return (string) ($this->data()['name'] ?? '');
    }

    public function author(): string
    {
        return (string) ($this->data()['author'] ?? '');
    }

    /** @return array<string, string> env_variable => Standardwert */
    public function defaults(): array
    {
        $out = [];
        foreach ((array) ($this->data()['variables'] ?? []) as $variable) {
            if (is_array($variable) && is_string($variable['env_variable'] ?? null)) {
                $out[$variable['env_variable']] = (string) ($variable['default_value'] ?? '');
            }
        }

        return $out;
    }
}

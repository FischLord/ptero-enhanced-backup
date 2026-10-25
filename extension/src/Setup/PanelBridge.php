<?php

namespace PebBackups\Setup;

/**
 * Alles, was der Assistent vom Panel braucht und was nicht zur oeffentlichen Extension-API gehoert
 * (Egg-Import, Serveranlage, Auswahllisten). Die echte Umsetzung ist PanelAdapter; Tests nutzen Fakes.
 */
interface PanelBridge
{
    /** @return array{egg_import: bool, egg_update: bool, server_create: bool, s3_probe: bool} */
    public function availability(): array;

    /** @return array{driver: ?string, endpoint: ?string, path_style: bool, max_part_size: int, bucket: ?string, key: ?string, secret: ?string, region: ?string} */
    public function backupFacts(): array;

    /** @return array{id: int, name: string}|null */
    public function findEgg(string $name, string $author): ?array;

    /** @return int ID des neuen Eggs */
    public function importEgg(string $json): int;

    public function updateEgg(int $eggId, string $json): void;

    /** @return array{id: int, image: string, startup: string, variables: array<string, string>} Variablen: env_variable => Standardwert */
    public function eggDetails(int $eggId): array;

    /** @return list<array{id: int, name: string, fqdn: string, free_allocations: int}> */
    public function nodes(): array;

    /** @return list<array{id: int, ip: string, alias: ?string, port: int}> nur freie */
    public function freeAllocations(int $nodeId): array;

    /** @return array{id: int, ip: string, alias: ?string, port: int, free: bool}|null */
    public function allocation(int $allocationId): ?array;

    /** @return list<array{id: int, username: string, email: string}> nur Root-Admins */
    public function users(string $query): array;

    /**
     * @param  array<string, mixed>  $input Eingabe fuer den Serveranlage-Dienst des Panels
     * @return array{id: int, uuid: string}
     *
     * @throws SetupException
     */
    public function createServer(array $input): array;
}

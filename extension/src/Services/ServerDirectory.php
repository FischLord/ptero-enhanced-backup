<?php

namespace PebBackups\Services;

use Pterodactyl\Models\Server;

/** Ordnet Server-UUIDs aus dem Gateway den Servern des Panels zu (Name, Node, Besitzer, Link). */
class ServerDirectory
{
    /**
     * @param  list<string>  $uuids
     * @return array<string, array{id: int, name: string, node: string|null, owner: string|null, backup_limit: int, backup_count: int}> keyed by lowercase UUID
     */
    public function find(array $uuids): array
    {
        $uuids = array_values(array_unique(array_map('strtolower', array_filter($uuids, 'is_string'))));
        if ($uuids === []) {
            return [];
        }

        $found = [];
        foreach (array_chunk($uuids, 500) as $chunk) {
            $servers = Server::query()
                ->with(['node:id,name', 'user:id,username'])
                // backup_count zaehlt wie das Panel beim Limit: laufende und erfolgreiche Backups, keine fehlgeschlagenen.
                ->withCount(['backups as backup_count' => fn ($query) => $query->nonFailed()])
                ->whereIn('uuid', $chunk)
                ->get(['id', 'uuid', 'name', 'node_id', 'owner_id', 'backup_limit']);

            foreach ($servers as $server) {
                $found[strtolower($server->uuid)] = [
                    'id' => (int) $server->id,
                    'name' => (string) $server->name,
                    'node' => $server->node?->name,
                    'owner' => $server->user?->username,
                    'backup_limit' => (int) $server->backup_limit,
                    'backup_count' => (int) $server->getAttribute('backup_count'),
                ];
            }
        }

        return $found;
    }
}

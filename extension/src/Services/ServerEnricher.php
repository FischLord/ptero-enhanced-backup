<?php

namespace PebBackups\Services;

/** Haengt an Gateway-Zeilen mit `server_uuid` das Panel-Objekt `server` (oder null bei unbekannten UUIDs). */
class ServerEnricher
{
    public function __construct(private readonly ServerDirectory $directory) {}

    /**
     * @param  list<array<string, mixed>>  $items
     * @return list<array<string, mixed>>
     */
    public function attach(array $items, string $key = 'server_uuid'): array
    {
        $uuids = [];
        foreach ($items as $item) {
            if (is_string($item[$key] ?? null)) {
                $uuids[] = $item[$key];
            }
        }
        $known = $this->directory->find($uuids);

        return array_map(function (array $item) use ($known, $key): array {
            $uuid = is_string($item[$key] ?? null) ? strtolower($item[$key]) : null;
            $item['server'] = $uuid !== null ? ($known[$uuid] ?? null) : null;

            return $item;
        }, array_values($items));
    }
}

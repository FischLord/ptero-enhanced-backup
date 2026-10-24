<?php

namespace PebBackups\Pro;

/**
 * Erweiterungspunkt fuer die Pro-Extension (`peb-backups-pro`).
 *
 * Pro traegt sich in seinem Provider hier ein (`boot()`). Weil Pro `peb-backups` in
 * `requires.extensions` fordert, laedt das Panel die Free-Extension zuerst; ist Pro nicht
 * aktiviert, wird sein Provider gar nicht geladen und die Registry bleibt leer. Mehr als
 * Container (Laravel) und Ladereihenfolge der Extensions ist dafuer nicht noetig.
 *
 * Die Oberflaeche fragt das ueber `GET /api/admin/extensions/peb-backups/edition` ab und
 * blendet damit die kleinen "Pro"-Hinweise aus.
 */
class ProRegistry
{
    /** @var array{id: string, version: string, features: list<string>}|null */
    private ?array $pro = null;

    /** @param list<string> $features Kennungen der Funktionen, z. B. "retention" */
    public function register(string $id, string $version, array $features = []): void
    {
        $this->pro = ['id' => $id, 'version' => $version, 'features' => array_values($features)];
    }

    public function isInstalled(): bool
    {
        return $this->pro !== null;
    }

    public function has(string $feature): bool
    {
        return $this->pro !== null && in_array($feature, $this->pro['features'], true);
    }

    /** @return array{installed: bool, id: string|null, version: string|null, features: list<string>} */
    public function info(): array
    {
        return [
            'installed' => $this->pro !== null,
            'id' => $this->pro['id'] ?? null,
            'version' => $this->pro['version'] ?? null,
            'features' => $this->pro['features'] ?? [],
        ];
    }
}

<?php

use PebBackups\Services\ServerDirectory;
use PebBackups\Services\ServerEnricher;

test('attaches panel servers and leaves unknown UUIDs as null', function () {
    $directory = new class extends ServerDirectory
    {
        public array $asked = [];

        public function find(array $uuids): array
        {
            $this->asked = $uuids;

            return ['aaaaaaaa-0000-0000-0000-000000000001' => ['id' => 7, 'name' => 'Survival', 'node' => 'wings', 'owner' => 'admin']];
        }
    };

    $items = (new ServerEnricher($directory))->attach([
        ['server_uuid' => 'AAAAAAAA-0000-0000-0000-000000000001', 'backup_count' => 1],
        ['server_uuid' => 'bbbbbbbb-0000-0000-0000-000000000002', 'backup_count' => 2],
        ['backup_count' => 3],
    ]);

    expect($items[0]['server']['name'])->toBe('Survival')
        ->and($items[0]['server']['id'])->toBe(7)
        ->and($items[1]['server'])->toBeNull()
        ->and($items[2]['server'])->toBeNull()
        ->and($directory->asked)->toHaveCount(2);
});

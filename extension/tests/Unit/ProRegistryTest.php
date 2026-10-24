<?php

use PebBackups\Pro\ProRegistry;

test('is empty until Pro registers itself', function () {
    $registry = new ProRegistry;

    expect($registry->isInstalled())->toBeFalse()
        ->and($registry->has('retention'))->toBeFalse()
        ->and($registry->info())->toBe(['installed' => false, 'id' => null, 'version' => null, 'features' => []]);
});

test('reports the registered Pro extension and its features', function () {
    $registry = new ProRegistry;
    $registry->register('peb-backups-pro', '1.0.0', ['retention']);

    expect($registry->isInstalled())->toBeTrue()
        ->and($registry->has('retention'))->toBeTrue()
        ->and($registry->has('other'))->toBeFalse()
        ->and($registry->info()['version'])->toBe('1.0.0');
});

<?php

use PebBackups\Http\Controllers\BackupsController;

test('builds a safe download filename', function (string $path, string $expected) {
    expect(BackupsController::downloadName($path))->toBe($expected);
})->with([
    ['world/level.dat', 'level.dat'],
    ['a\\b\\c.txt', 'c.txt'],
    [" lead and trail \t", ' lead and trail '],
    ["evil\r\nname.txt", 'evilname.txt'],
    ["dir/\x00\x1f", 'download'],
    ['dir/', 'dir'],
    ['', 'download'],
]);

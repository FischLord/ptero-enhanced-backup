<?php

use Illuminate\Support\Facades\Route;
use PebBackups\Http\Controllers\BackupsController;
use PebBackups\Http\Controllers\EditionController;
use PebBackups\Http\Controllers\MaintenanceController;
use PebBackups\Http\Controllers\NotifyController;
use PebBackups\Http\Controllers\OverviewController;
use PebBackups\Http\Controllers\SecurityController;
use PebBackups\Http\Controllers\ServersController;
use PebBackups\Http\Controllers\SetupController;
use PebBackups\Http\Controllers\TrashController;

// Prefix: /api/admin/extensions/peb-backups (nur Root-Admins, Session oder Account-API-Key).
// Alle Routen sind reine Proxys zur Admin-API des Gateways (docs/admin-api.md).
Route::get('/overview', OverviewController::class)->name('overview');
Route::get('/servers', ServersController::class)->name('servers');
Route::get('/backups', [BackupsController::class, 'index'])->name('backups');
Route::get('/backups/{backup}/files', [BackupsController::class, 'files'])->whereUuid('backup')->name('backups.files');
Route::get('/backups/{backup}/file', [BackupsController::class, 'download'])->whereUuid('backup')->name('backups.file');
Route::get('/maintenance', [MaintenanceController::class, 'index'])->name('maintenance');
Route::post('/maintenance/run', [MaintenanceController::class, 'run'])->name('maintenance.run');
// Restic-Passwort: Bestaetigung, Notfallblatt-Daten, zweiter Schluessel.
Route::get('/password-ack', [SecurityController::class, 'passwordAck'])->name('password-ack');
Route::put('/password-ack', [SecurityController::class, 'setPasswordAck'])->name('password-ack.set');
Route::get('/emergency-sheet', [SecurityController::class, 'emergencySheet'])->name('emergency-sheet');
Route::get('/keys', [SecurityController::class, 'keys'])->name('keys');
Route::post('/keys', [SecurityController::class, 'createKey'])->name('keys.create');
Route::delete('/keys/{key}', [SecurityController::class, 'deleteKey'])->where('key', '[0-9a-f]{8,64}')->name('keys.delete');
// Benachrichtigungen: konfigurierte Kanaele (geschwaerzt) anzeigen und Testnachricht ausloesen (Gateway 0.3).
Route::get('/notify', [NotifyController::class, 'index'])->name('notify');
Route::post('/notify/test', [NotifyController::class, 'test'])->name('notify.test');
Route::get('/edition', EditionController::class)->name('edition');

// Einrichtungsassistent. POST-Routen legen Eggs/Server an bzw. senden Testanfragen; wie alle Routen hier nur fuer Root-Admins.
Route::get('/setup', [SetupController::class, 'index'])->name('setup');
Route::post('/setup/egg', [SetupController::class, 'egg'])->name('setup.egg');
Route::get('/setup/nodes', [SetupController::class, 'nodes'])->name('setup.nodes');
Route::get('/setup/nodes/{node}/allocations', [SetupController::class, 'allocations'])->whereNumber('node')->name('setup.allocations');
Route::get('/setup/users', [SetupController::class, 'users'])->name('setup.users');
Route::post('/setup/server', [SetupController::class, 'server'])->name('setup.server');
Route::post('/setup/self-test', [SetupController::class, 'selfTest'])->name('setup.self-test');

// Papierkorb: im Panel geloeschte Backups und Backups geloeschter Server (Gateway 0.2.0).
Route::get('/trash', [TrashController::class, 'index'])->name('trash');
Route::post('/trash/{backup}/restore', [TrashController::class, 'restore'])->whereUuid('backup')->name('trash.restore');
Route::delete('/trash/{backup}', [TrashController::class, 'purge'])->whereUuid('backup')->name('trash.purge');

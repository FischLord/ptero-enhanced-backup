<?php

use Illuminate\Support\Facades\Route;
use PebBackups\Http\Controllers\BackupsController;
use PebBackups\Http\Controllers\EditionController;
use PebBackups\Http\Controllers\MaintenanceController;
use PebBackups\Http\Controllers\OverviewController;
use PebBackups\Http\Controllers\ServersController;

// Prefix: /api/admin/extensions/peb-backups (nur Root-Admins, Session oder Account-API-Key).
// Alle Routen sind reine Proxys zur Admin-API des Gateways (docs/admin-api.md).
Route::get('/overview', OverviewController::class)->name('overview');
Route::get('/servers', ServersController::class)->name('servers');
Route::get('/backups', [BackupsController::class, 'index'])->name('backups');
Route::get('/backups/{backup}/files', [BackupsController::class, 'files'])->whereUuid('backup')->name('backups.files');
Route::get('/backups/{backup}/file', [BackupsController::class, 'download'])->whereUuid('backup')->name('backups.file');
Route::get('/maintenance', [MaintenanceController::class, 'index'])->name('maintenance');
Route::post('/maintenance/run', [MaintenanceController::class, 'run'])->name('maintenance.run');
Route::get('/edition', EditionController::class)->name('edition');

<?php

namespace PebBackups;

use Illuminate\Http\Client\Factory as HttpFactory;
use PebBackups\Gateway\GatewayClient;
use PebBackups\Gateway\GatewayConfig;
use PebBackups\Pro\ProRegistry;
use Pterodactyl\Extensions\ExtensionProvider;
use Pterodactyl\Services\Extensions\ExtensionSettingDefinition;
use Pterodactyl\Services\Extensions\ExtensionSettingsDefinition;
use Pterodactyl\Services\Extensions\ExtensionSettingsRegistry;

class PebBackupsProvider extends ExtensionProvider
{
    public function register(): void
    {
        // Erweiterungspunkt fuer peb-backups-pro (siehe ProRegistry).
        $this->app->singleton(ProRegistry::class);

        // Der Client wird erst beim ersten Zugriff gebaut, damit frisch gespeicherte Einstellungen gelten.
        $this->app->bind(GatewayConfig::class, function ($app): GatewayConfig {
            $settings = $app->make(ExtensionSettingsRegistry::class)->get('peb-backups');

            return GatewayConfig::fromSettings(fn (string $key) => $settings?->get($key));
        });
        $this->app->bind(GatewayClient::class, fn ($app) => new GatewayClient(
            $app->make(HttpFactory::class),
            $app->make(GatewayConfig::class),
        ));
    }

    public function boot(): void
    {
        // routes/admin.php -> /api/admin/extensions/peb-backups (nur Root-Admins)
        $this->registerApiRoutes();
        $this->loadExtensionTranslations();

        $this->registerSettings(new ExtensionSettingsDefinition($this->settings(), [
            ExtensionSettingDefinition::make('gateway_url', 'gateway_url', '', ['nullable', 'url:http,https', 'max:255'])
                ->label('Gateway base URL')
                ->help('Address of the PEB gateway as seen from the panel server, for example http://10.0.0.5:9000 (without /admin/v1).')
                ->normalizeUsing(fn ($value) => is_string($value) ? rtrim(trim($value), '/') : $value),
            ExtensionSettingDefinition::make('admin_token', 'admin_token', '', ['nullable', 'string', 'max:512'])
                ->label('Gateway admin token')
                ->help('Value of PEB_ADMIN_TOKEN. Stored encrypted and never sent to the browser. Leave blank to keep the saved token.')
                ->secret(),
            ExtensionSettingDefinition::make('price_per_tb', 'price_per_tb', 6, ['required', 'numeric', 'min:0', 'max:1000000'])
                ->label('Storage price per TB and month')
                ->help('Used to compute the money saved per month from the saved storage.')
                ->field('number'),
            ExtensionSettingDefinition::make('currency', 'currency', 'EUR', ['required', 'string', 'regex:/^[A-Za-z]{3}$/'])
                ->label('Currency (ISO 4217)')
                ->help('Three letters, for example EUR or USD.')
                ->normalizeUsing(fn ($value) => is_string($value) ? strtoupper(trim($value)) : $value),
            ExtensionSettingDefinition::make('warn_hours', 'warn_hours', 26, ['required', 'integer', 'min:1', 'max:8760'])
                ->label('Warn after hours without a successful backup')
                ->help('A server whose last successful backup is older than this is flagged in the server table.')
                ->field('number'),
        ]));
    }
}

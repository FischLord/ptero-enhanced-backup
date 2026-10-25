<?php

namespace PebBackups\Services;

use Closure;
use Illuminate\Support\Facades\Cache;
use Illuminate\Support\Facades\Log;
use PebBackups\Gateway\GatewayClient;
use PebBackups\Gateway\GatewayException;
use Pterodactyl\Models\Server;
use Throwable;

/**
 * Meldet dem Gateway, welche Server das Panel nicht mehr kennt (Panel-Luecke #2564: Das Loeschen
 * eines Servers entfernt seine Backups nur per Datenbank-Kaskade, das Gateway bekommt nichts mit).
 * Das Gateway legt deren Backups dann in den Papierkorb.
 *
 * Zwei Wege: sofort ueber das Server-Loesch-Ereignis des Panels (markDeleted) und als Abgleich
 * der kompletten Serverliste (reconcile; stuendlich, beim Oeffnen des Dashboards). Der Abgleich
 * faengt auch verpasste Ereignisse, Fehlalarme hebt das Gateway selbst wieder auf.
 */
class ServerReconciler
{
    public const THROTTLE_SECONDS = 300;

    /** @var Closure(): list<string> */
    private readonly Closure $uuids;

    /** @param  (Closure(): list<string>)|null  $uuids  Quelle der Panel-UUIDs (Tests); sonst die Server-Tabelle */
    public function __construct(private readonly GatewayClient $gateway, ?Closure $uuids = null)
    {
        $this->uuids = $uuids ?? fn (): array => Server::query()->pluck('uuid')->map(fn ($u) => strtolower((string) $u))->all();
    }

    /**
     * Schickt alle Server-UUIDs des Panels. Eine leere Liste wird nie gesendet (Sicherheitsnetz,
     * das Gateway ignoriert sie ausserdem).
     *
     * @return array<string, mixed>|null Antwort des Gateways, null wenn nichts gesendet wurde
     *
     * @throws GatewayException
     */
    public function reconcile(): ?array
    {
        $uuids = array_values(array_unique(($this->uuids)()));
        if ($uuids === []) {
            return null;
        }

        return $this->gateway->post('servers/reconcile', ['server_uuids' => $uuids]);
    }

    /** Wie reconcile(), hoechstens alle THROTTLE_SECONDS und ohne je zu werfen (das Dashboard darf nie daran scheitern). */
    public function reconcileQuietly(): void
    {
        try {
            if (! Cache::add('peb-backups:reconcile', 1, self::THROTTLE_SECONDS)) {
                return;
            }
            $this->reconcile();
        } catch (Throwable $e) {
            Log::warning('PEB server reconcile failed.', ['error' => $e->getMessage()]);
        }
    }

    /** Ein Server wurde im Panel geloescht; das Gateway verschiebt seine Backups in den Papierkorb. */
    public function markDeleted(string $serverUuid): void
    {
        try {
            $this->gateway->post('servers/'.strtolower($serverUuid).'/deleted', []);
        } catch (Throwable $e) {
            // Der naechste Abgleich holt es nach.
            Log::warning('PEB could not report a deleted server.', ['server' => $serverUuid, 'error' => $e->getMessage()]);
        }
    }
}

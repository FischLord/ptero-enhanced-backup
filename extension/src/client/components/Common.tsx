import type { ReactNode } from 'react';
import { Badge, PanelLink, Tooltip } from '@pterodactyl/sdk';
import { useT } from '../i18n';
import type { MaintenanceJob, PanelServerRef, ServerHealth } from '../types';

type Tone = 'neutral' | 'success' | 'warning' | 'danger' | 'info' | 'accent';

export function Section({ title, actions, children }: { title: string; actions?: ReactNode; children: ReactNode }) {
    return (
        <section className={'peb:flex peb:flex-col peb:gap-3'}>
            <div className={'peb:flex peb:items-center peb:justify-between peb:gap-3'}>
                <h2 className={'peb:text-lg peb:font-semibold peb:text-foreground'}>{title}</h2>
                {actions}
            </div>
            {children}
        </section>
    );
}

export function JobResultBadge({ result }: { result: MaintenanceJob['result'] }) {
    const { t } = useT();
    const tone: Record<MaintenanceJob['result'], Tone> = {
        ok: 'success',
        failed: 'danger',
        skipped: 'warning',
        queued: 'info',
        running: 'info',
    };

    return (
        <Badge tone={tone[result]} variant={'soft'}>
            {t(`result_${result}`)}
        </Badge>
    );
}

export function HealthBadge({ health, warnHours }: { health: ServerHealth; warnHours: number }) {
    const { t } = useT();
    if (health === 'ok') return null;
    const tone: Record<Exclude<ServerHealth, 'ok'>, Tone> = { warning: 'warning', failed: 'danger', never: 'neutral' };
    const label = { warning: t('health_overdue'), failed: t('health_failed'), never: t('health_never') }[health];
    const hint = { warning: t('health_overdue_hint', { hours: warnHours }), failed: t('health_failed_hint'), never: t('health_never_hint') }[health];

    return (
        <Tooltip content={hint}>
            <span>
                <Badge tone={tone[health]} variant={'soft'}>
                    {label}
                </Badge>
            </span>
        </Tooltip>
    );
}

/** Servername mit Link zur Admin-Seite; unbekannte UUIDs werden als solche gekennzeichnet. */
export function ServerLabel({ server, uuid, compact }: { server: PanelServerRef | null; uuid: string; compact?: boolean }) {
    const { t } = useT();
    if (!server) {
        return (
            <div className={'peb:flex peb:flex-col'}>
                <span className={'peb:italic peb:text-muted-foreground'}>{t('unknown_server')}</span>
                <span className={'peb:font-mono peb:text-xs peb:text-muted-foreground'}>{uuid}</span>
            </div>
        );
    }

    return (
        <div className={'peb:flex peb:flex-col'}>
            <PanelLink destination={{ to: '/panel/servers/$id', params: { id: String(server.id) } }} className={'peb:font-medium peb:text-accent peb:hover:underline'}>
                {server.name}
            </PanelLink>
            {!compact && (
                <span className={'peb:text-xs peb:text-muted-foreground'}>
                    {[server.node, server.owner].filter(Boolean).join(' · ')}
                </span>
            )}
        </div>
    );
}

export function Muted({ children }: { children: ReactNode }) {
    return <span className={'peb:text-muted-foreground'}>{children}</span>;
}

/** Text mit Erklaerung im Tooltip (und als natives title-Attribut fuer Touch und Tests). */
export function Hint({ text, children }: { text: string; children: ReactNode }) {
    return (
        <Tooltip content={text}>
            <span title={text} className={'peb:cursor-help peb:underline peb:decoration-dotted peb:underline-offset-4'}>
                {children}
            </span>
        </Tooltip>
    );
}

import { useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { Badge, Button, PageContentBlock, PageHeading } from '@pterodactyl/sdk';
import { getOverview, getServers, keys } from '../api';
import { Loaded } from '../components/Loaded';
import { Section } from '../components/Common';
import SavingsTiles from '../components/SavingsTiles';
import StorageTargetCard from '../components/StorageTargetCard';
import OffsiteCard from '../components/OffsiteCard';
import PasswordCard from '../components/PasswordCard';
import EmergencyKeysCard from '../components/EmergencyKeysCard';
import NotificationsCard from '../components/NotificationsCard';
import HealthCard from '../components/HealthCard';
import HistoryChart from '../components/HistoryChart';
import ServersTable from '../components/ServersTable';
import ServerBackups from '../components/ServerBackups';
import MaintenanceView from '../components/MaintenanceView';
import TrashView from '../components/TrashView';
import { formatRelative } from '../format';
import { useT } from '../i18n';
import type { ServerRow } from '../types';

type Tab = 'overview' | 'servers' | 'maintenance' | 'trash';

function Overview() {
    const { t, locale } = useT();
    const query = useQuery({ queryKey: keys.overview(30), queryFn: () => getOverview(30), refetchInterval: 30000, retry: false });

    return (
        <Loaded query={query}>
            {(overview) => (
                <div className={'peb:flex peb:flex-col peb:gap-6'}>
                    <PasswordCard />
                    <Section title={t('section_savings')}>
                        <SavingsTiles overview={overview} />
                    </Section>
                    <div className={'peb:grid peb:grid-cols-1 peb:gap-6 peb:xl:grid-cols-2'}>
                        <StorageTargetCard overview={overview} />
                        <OffsiteCard overview={overview} />
                    </div>
                    <div className={'peb:grid peb:grid-cols-1 peb:gap-6 peb:xl:grid-cols-2'}>
                        <HealthCard overview={overview} />
                        <HistoryChart history={overview.history} />
                    </div>
                    <EmergencyKeysCard />
                    <NotificationsCard />
                    <p className={'peb:text-xs peb:text-muted-foreground'}>{t('updated', { time: formatRelative(overview.generated_at, locale) })}</p>
                </div>
            )}
        </Loaded>
    );
}

function Servers() {
    const { t } = useT();
    const [server, setServer] = useState<ServerRow | null>(null);
    const query = useQuery({ queryKey: keys.servers(), queryFn: getServers, refetchInterval: 60000, retry: false });

    if (server) {
        return (
            <Section
                title={t('backups_of', { name: server.server?.name ?? t('unknown_server') })}
                actions={
                    <Button size={'small'} isSecondary onClick={() => setServer(null)}>
                        {t('back_to_servers')}
                    </Button>
                }
            >
                <ServerBackups serverUuid={server.server_uuid} />
            </Section>
        );
    }

    return (
        <Loaded query={query}>
            {(data) => <ServersTable rows={data.items} warnHours={data.warn_hours} onOpen={setServer} />}
        </Loaded>
    );
}

export default function BackupsScreen() {
    const { t } = useT();
    const [tab, setTab] = useState<Tab>('overview');
    // Gleicher Schluessel wie die Uebersicht: eine Abfrage, die auch die Anzahl im Papierkorb liefert.
    const overview = useQuery({ queryKey: keys.overview(30), queryFn: () => getOverview(30), refetchInterval: 30000, retry: false });
    const trashCount = overview.data?.summary.trash?.count ?? 0;
    const tabs: { id: Tab; label: string }[] = [
        { id: 'overview', label: t('tab_overview') },
        { id: 'servers', label: t('tab_servers') },
        { id: 'maintenance', label: t('tab_maintenance') },
        { id: 'trash', label: t('tab_trash') },
    ];

    return (
        <PageContentBlock title={t('page_title')}>
            <div className={'peb:flex peb:flex-col peb:gap-5'}>
                <PageHeading title={t('page_title')} description={t('page_description')} />
                <div role={'tablist'} className={'peb:flex peb:flex-wrap peb:gap-2'}>
                    {tabs.map((item) => (
                        <Button key={item.id} role={'tab'} aria-selected={tab === item.id} size={'small'} isSecondary={tab !== item.id} onClick={() => setTab(item.id)}>
                            {item.label}
                            {item.id === 'trash' && trashCount > 0 && (
                                <span className={'peb:ml-2'} title={t('trash_badge_hint', { count: trashCount })}>
                                    <Badge tone={'warning'} variant={'soft'}>
                                        {trashCount}
                                    </Badge>
                                </span>
                            )}
                        </Button>
                    ))}
                </div>
                {tab === 'overview' && <Overview />}
                {tab === 'servers' && <Servers />}
                {tab === 'maintenance' && <MaintenanceView />}
                {tab === 'trash' && <TrashView />}
            </div>
        </PageContentBlock>
    );
}

import { useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { Button, PageContentBlock, PageHeading } from '@pterodactyl/sdk';
import { getOverview, getServers, keys } from '../api';
import { Loaded } from '../components/Loaded';
import { Section } from '../components/Common';
import SavingsTiles from '../components/SavingsTiles';
import StorageTargetCard from '../components/StorageTargetCard';
import HealthCard from '../components/HealthCard';
import HistoryChart from '../components/HistoryChart';
import ServersTable from '../components/ServersTable';
import ServerBackups from '../components/ServerBackups';
import MaintenanceView from '../components/MaintenanceView';
import { formatRelative } from '../format';
import { useT } from '../i18n';
import type { ServerRow } from '../types';

type Tab = 'overview' | 'servers' | 'maintenance';

function Overview() {
    const { t, locale } = useT();
    const query = useQuery({ queryKey: keys.overview(30), queryFn: () => getOverview(30), refetchInterval: 30000, retry: false });

    return (
        <Loaded query={query}>
            {(overview) => (
                <div className={'peb:flex peb:flex-col peb:gap-6'}>
                    <Section title={t('section_savings')}>
                        <SavingsTiles overview={overview} />
                    </Section>
                    <StorageTargetCard overview={overview} />
                    <div className={'peb:grid peb:grid-cols-1 peb:gap-6 peb:xl:grid-cols-2'}>
                        <HealthCard overview={overview} />
                        <HistoryChart history={overview.history} />
                    </div>
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
    const tabs: { id: Tab; label: string }[] = [
        { id: 'overview', label: t('tab_overview') },
        { id: 'servers', label: t('tab_servers') },
        { id: 'maintenance', label: t('tab_maintenance') },
    ];

    return (
        <PageContentBlock title={t('page_title')}>
            <div className={'peb:flex peb:flex-col peb:gap-5'}>
                <PageHeading title={t('page_title')} description={t('page_description')} />
                <div role={'tablist'} className={'peb:flex peb:flex-wrap peb:gap-2'}>
                    {tabs.map((item) => (
                        <Button key={item.id} role={'tab'} aria-selected={tab === item.id} size={'small'} isSecondary={tab !== item.id} onClick={() => setTab(item.id)}>
                            {item.label}
                        </Button>
                    ))}
                </div>
                {tab === 'overview' && <Overview />}
                {tab === 'servers' && <Servers />}
                {tab === 'maintenance' && <MaintenanceView />}
            </div>
        </PageContentBlock>
    );
}

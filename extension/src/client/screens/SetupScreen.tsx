import { useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { PageContentBlock, PageHeading } from '@pterodactyl/sdk';
import { getSetup, keys } from '../api';
import { Section } from '../components/Common';
import { Loaded } from '../components/Loaded';
import CheckList from '../components/setup/CheckList';
import CredentialsPanel from '../components/setup/CredentialsPanel';
import EggCard from '../components/setup/EggCard';
import SelfTestCard from '../components/setup/SelfTestCard';
import ServerForm from '../components/setup/ServerForm';
import SnippetBlock from '../components/setup/SnippetBlock';
import { useT } from '../i18n';
import type { CreatedGatewayServer } from '../types';

export default function SetupScreen() {
    const { t } = useT();
    // Die erzeugten Zugangsdaten bleiben nur hier im Speicher (nicht im Query-Cache) und verschwinden mit "gesichert" oder beim Verlassen.
    const [created, setCreated] = useState<CreatedGatewayServer | null>(null);
    const query = useQuery({ queryKey: keys.setup(), queryFn: getSetup, retry: false });

    return (
        <PageContentBlock title={t('setup_title')}>
            <div className={'peb:flex peb:flex-col peb:gap-6'}>
                <PageHeading title={t('setup_title')} description={t('setup_description')} />
                <Loaded query={query}>
                    {(setup) => (
                        <>
                            <Section title={t('setup_step_precheck')}>
                                <p className={'peb:text-sm peb:text-muted-foreground'}>{t('setup_precheck_intro')}</p>
                                <CheckList checks={setup.checks} />
                                <p className={'peb:text-sm peb:font-medium'}>{t(setup.ready ? 'setup_precheck_ready' : 'setup_precheck_not_ready')}</p>
                            </Section>
                            <Section title={t('setup_step_egg')}>
                                <EggCard setup={setup} />
                            </Section>
                            <Section title={t('setup_step_server')}>
                                {created ? <CredentialsPanel created={created} onDismiss={() => setCreated(null)} /> : <ServerForm setup={setup} onCreated={setCreated} />}
                            </Section>
                            <Section title={t('setup_step_snippets')}>
                                <p className={'peb:text-sm peb:text-muted-foreground'}>{t('setup_snippets_intro')}</p>
                                {!created && <p className={'peb:text-xs peb:text-muted-foreground'}>{t('setup_snippets_placeholders')}</p>}
                                <SnippetBlock title={t('setup_snippet_env')} text={(created ?? setup).snippets.env} />
                                <SnippetBlock title={t('setup_snippet_wings')} text={(created ?? setup).snippets.wings} />
                            </Section>
                            <Section title={t('setup_step_selftest')}>
                                <SelfTestCard />
                            </Section>
                        </>
                    )}
                </Loaded>
            </div>
        </PageContentBlock>
    );
}

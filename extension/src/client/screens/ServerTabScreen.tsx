import { Alert, Card, useCurrentResource } from '@pterodactyl/sdk';
import ServerBackups from '../components/ServerBackups';
import { useT } from '../i18n';

/** Tab "Backups" auf der Admin-Seite eines Servers (parent: admin.server). */
export default function ServerTabScreen() {
    const { t } = useT();
    const current = useCurrentResource();
    const uuid = current?.kind === 'admin.server' ? current.resource.attributes.uuid : null;

    return (
        <Card title={t('page_title')}>
            {uuid ? <ServerBackups serverUuid={uuid} /> : <Alert type={'warning'}>{t('no_server_context')}</Alert>}
        </Card>
    );
}

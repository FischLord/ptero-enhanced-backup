import { Alert, Button } from '@pterodactyl/sdk';
import { useT } from '../../i18n';
import type { CreatedGatewayServer } from '../../types';
import SnippetBlock from './SnippetBlock';

/** Einmalige Anzeige der erzeugten Zugangsdaten. Nach "gesichert" sind sie in der Oberflaeche nicht mehr vorhanden. */
export default function CredentialsPanel({ created, onDismiss }: { created: CreatedGatewayServer; onDismiss: () => void }) {
    const { t } = useT();
    const c = created.credentials;

    return (
        <div className={'peb:flex peb:flex-col peb:gap-3'}>
            <Alert type={'success'} title={t('setup_created_title')}>
                {t('setup_created_text', { name: created.server.name })}
            </Alert>
            <Alert type={'warning'} title={t('setup_secrets_title')}>
                {t('setup_secrets_warning')}
            </Alert>
            <SnippetBlock title={t('setup_secret_access_key')} text={c.access_key} />
            <SnippetBlock title={t('setup_secret_secret_key')} text={c.secret_key} />
            <SnippetBlock title={t('setup_secret_admin_token')} text={c.admin_token} />
            <SnippetBlock title={t('setup_secret_restic_password')} text={c.restic_password} />
            <Alert type={'info'}>{t('setup_password_hint')}</Alert>
            <p className={'peb:text-sm peb:text-muted-foreground'}>{t('setup_gateway_address', { url: created.gateway_url })}</p>
            <div>
                <Button color={'primary'} onClick={onDismiss}>
                    {t('setup_stored')}
                </Button>
            </div>
        </div>
    );
}

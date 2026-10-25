import { useMutation } from '@tanstack/react-query';
import { Alert, Button } from '@pterodactyl/sdk';
import { runSelfTest } from '../../api';
import { useT } from '../../i18n';
import { setupErrorText } from './setupErrors';
import CheckList from './CheckList';

export default function SelfTestCard() {
    const { t } = useT();
    const test = useMutation({ mutationFn: runSelfTest });

    return (
        <div className={'peb:flex peb:flex-col peb:gap-3'}>
            <p className={'peb:text-sm peb:text-muted-foreground'}>{t('setup_selftest_intro')}</p>
            <div>
                <Button color={'primary'} disabled={test.isPending} onClick={() => test.mutate()}>
                    {test.isPending ? t('setup_selftest_running') : t('setup_selftest_run')}
                </Button>
            </div>
            {test.isError && <Alert type={'danger'}>{setupErrorText(test.error, t, 'setup_selftest_error')}</Alert>}
            {test.data && (
                <>
                    <Alert type={test.data.ok ? 'success' : 'danger'}>{t(test.data.ok ? 'setup_selftest_ok' : 'setup_selftest_failed')}</Alert>
                    <CheckList checks={test.data.checks} />
                </>
            )}
        </div>
    );
}

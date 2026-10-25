import { useMutation, useQueryClient } from '@tanstack/react-query';
import { Alert, Button, toast } from '@pterodactyl/sdk';
import { EXTENSION_ID, importEgg } from '../../api';
import { useT } from '../../i18n';
import type { SetupState } from '../../types';
import { setupErrorText } from './setupErrors';

export default function EggCard({ setup }: { setup: SetupState }) {
    const { t } = useT();
    const queryClient = useQueryClient();
    const mutation = useMutation({
        mutationFn: (mode: 'import' | 'update') => importEgg(mode),
        onSuccess: (result) => {
            toast.success(t(result.action === 'import' ? 'setup_egg_imported' : 'setup_egg_updated'));
            void queryClient.invalidateQueries({ queryKey: [EXTENSION_ID, 'setup'] });
        },
        onError: (error) => {
            toast.error(setupErrorText(error, t, 'setup_egg_failed'));
            void queryClient.invalidateQueries({ queryKey: [EXTENSION_ID, 'setup'] });
        },
    });

    if (!setup.available.egg_import && !(setup.egg.exists && setup.available.egg_update)) {
        return (
            <Alert type={'warning'} title={t('setup_manual_egg_title')}>
                {t('setup_manual_egg')}
            </Alert>
        );
    }

    if (setup.egg.error) {
        return <Alert type={'danger'}>{t('setup_egg_error', { code: setup.egg.error })}</Alert>;
    }

    return (
        <div className={'peb:flex peb:flex-col peb:gap-3'}>
            <p className={'peb:text-sm peb:text-muted-foreground'}>{t('setup_egg_intro')}</p>
            <div className={'peb:flex peb:flex-wrap peb:items-center peb:gap-3'}>
                {setup.egg.exists ? (
                    <>
                        <p className={'peb:text-sm'}>{t('setup_egg_exists', { id: setup.egg.id ?? 0 })}</p>
                        {setup.available.egg_update && (
                            <Button isSecondary disabled={mutation.isPending} onClick={() => mutation.mutate('update')}>
                                {t('setup_egg_update')}
                            </Button>
                        )}
                        <p className={'peb:w-full peb:text-xs peb:text-muted-foreground'}>{t('setup_egg_update_note')}</p>
                    </>
                ) : (
                    <>
                        <p className={'peb:text-sm'}>{t('setup_egg_missing')}</p>
                        <Button color={'primary'} disabled={mutation.isPending || !setup.available.egg_import} onClick={() => mutation.mutate('import')}>
                            {t('setup_egg_import')}
                        </Button>
                    </>
                )}
            </div>
        </div>
    );
}

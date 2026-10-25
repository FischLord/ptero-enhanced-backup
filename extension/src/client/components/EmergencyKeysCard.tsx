import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Alert, Badge, Button, Card, Dialog, Table, toast, type TableColumn } from '@pterodactyl/sdk';
import { createRepoKey, deleteRepoKey, errorInfo, getRepoKeys, keys } from '../api';
import { formatDateTime } from '../format';
import { useT } from '../i18n';
import type { CreatedKey, RepoKey } from '../types';
import { Loaded } from './Loaded';
import SnippetBlock from './setup/SnippetBlock';

/** Einmalige Anzeige des neuen Schluessels. Nach "gesichert" ist das Passwort in der Oberflaeche nicht mehr vorhanden. */
export function CreatedKeyPanel({ created, onDone }: { created: CreatedKey; onDone: () => void }) {
    const { t } = useT();

    return (
        <div className={'peb:flex peb:flex-col peb:gap-3'}>
            <Alert type={'warning'} title={t('keys_created_title')}>
                {t('keys_created_warning')}
            </Alert>
            <SnippetBlock title={t('keys_created_password')} text={created.password} />
            <p className={'peb:text-xs peb:text-muted-foreground'}>
                {created.short_id} · {created.label}
            </p>
            <div>
                <Button color={'primary'} onClick={onDone}>
                    {t('keys_created_done')}
                </Button>
            </div>
        </div>
    );
}

/** Zweiter restic-Schluessel (Hoster-Notfallschluessel): erzeugen, einmal anzeigen, auflisten, loeschen. */
export default function EmergencyKeysCard() {
    const { t, locale } = useT();
    const queryClient = useQueryClient();
    const [label, setLabel] = useState('');
    const [created, setCreated] = useState<CreatedKey | null>(null);
    const [toDelete, setToDelete] = useState<RepoKey | null>(null);
    const query = useQuery({ queryKey: keys.repoKeys(), queryFn: getRepoKeys, retry: false, refetchOnWindowFocus: false });

    const create = useMutation({
        gcTime: 0,
        mutationFn: () => createRepoKey(label.trim()),
        onSuccess: (data) => {
            setCreated(data);
            setLabel('');
            void queryClient.invalidateQueries({ queryKey: keys.repoKeys() });
        },
        onError: (error) => toast.error(errorInfo(error).message || t('keys_create_failed')),
    });
    const remove = useMutation({
        mutationFn: (id: string) => deleteRepoKey(id),
        onSuccess: () => {
            toast.success(t('keys_deleted'));
            void queryClient.invalidateQueries({ queryKey: keys.repoKeys() });
        },
        onError: (error) => toast.error(errorInfo(error).message || t('keys_delete_failed')),
        onSettled: () => setToDelete(null),
    });

    // Gateway vor 0.2.0: Die Verwaltung gibt es dort nicht; der Bereich bleibt mit einem Hinweis stehen.
    if (query.isError && ['api_disabled', 'not_supported', 'gateway_error'].includes(errorInfo(query.error).code)) {
        return (
            <Card title={t('keys_title')}>
                <p className={'peb:text-sm peb:text-muted-foreground'}>{t('keys_unsupported')}</p>
            </Card>
        );
    }

    const columns: TableColumn<RepoKey>[] = [
        { id: 'id', label: t('keys_col_id'), cell: (k) => <span className={'peb:font-mono peb:text-xs'}>{k.short_id}</span> },
        { id: 'label', label: t('keys_col_label'), cell: (k) => k.label || k.host || '–' },
        { id: 'created', label: t('keys_col_created'), cell: (k) => formatDateTime(k.created_at, locale) },
        {
            id: 'state',
            label: t('keys_col_state'),
            cell: (k) => (
                <Badge tone={k.current ? 'success' : k.emergency ? 'info' : 'neutral'} variant={'soft'}>
                    {k.current ? t('keys_state_current') : k.emergency ? t('keys_state_emergency') : t('keys_state_other')}
                </Badge>
            ),
        },
        {
            id: 'actions',
            label: t('keys_col_actions'),
            cell: (k) =>
                k.current ? null : (
                    <Button size={'xsmall'} isSecondary onClick={() => setToDelete(k)}>
                        {t('keys_delete')}
                    </Button>
                ),
        },
    ];

    return (
        <Card title={t('keys_title')}>
            <div className={'peb:flex peb:flex-col peb:gap-3'}>
                <p className={'peb:text-sm peb:text-muted-foreground'}>{t('keys_intro')}</p>
                {created ? (
                    <CreatedKeyPanel created={created} onDone={() => {
                            setCreated(null);
                            create.reset();
                        }} />
                ) : (
                    <form
                        className={'peb:flex peb:flex-wrap peb:items-end peb:gap-2'}
                        onSubmit={(event) => {
                            event.preventDefault();
                            create.mutate();
                        }}
                    >
                        <label className={'peb:flex peb:flex-col peb:gap-1 peb:text-sm'}>
                            <span>{t('keys_label')}</span>
                            <input
                                value={label}
                                maxLength={64}
                                placeholder={t('keys_label_placeholder')}
                                onChange={(event) => setLabel(event.target.value)}
                                className={'peb:rounded-md peb:border peb:border-border peb:bg-background peb:px-2 peb:py-1'}
                            />
                        </label>
                        <Button type={'submit'} color={'primary'} disabled={create.isPending}>
                            {t('keys_create')}
                        </Button>
                    </form>
                )}
                <Loaded query={query}>
                    {(data) => <Table rows={data.keys} columns={columns} keyOf={(k) => k.id} isFetching={query.isFetching} emptyState={'–'} />}
                </Loaded>
            </div>
            <Dialog.Confirm
                open={toDelete !== null}
                onClose={() => setToDelete(null)}
                title={t('keys_delete_title')}
                confirm={t('keys_delete_confirm')}
                pending={remove.isPending}
                onConfirmed={() => {
                    if (toDelete) remove.mutate(toDelete.id);
                }}
            >
                <p>{t('keys_delete_text', { id: toDelete?.short_id ?? '' })}</p>
            </Dialog.Confirm>
        </Card>
    );
}

import { useMutation, useQuery } from '@tanstack/react-query';
import { Alert, Badge, Button, Card, toast } from '@pterodactyl/sdk';
import { errorInfo, getNotifyConfig, keys, sendTestNotification } from '../api';
import { useT } from '../i18n';
import type { NotifyChannel, NotifyTestResult } from '../types';
import { Loaded } from './Loaded';
import { Muted } from './Common';

export const NOTIFY_DOCS_URL = 'https://github.com/FischLord/ptero-enhanced-backup/blob/main/gateway/README.md#notifications';

/** Gateways vor 0.3 kennen die Routen nicht; die Extension meldet das als `not_supported` (oder 404). */
export function isNotifyUnsupported(error: unknown): boolean {
    const info = errorInfo(error);

    return info.code === 'not_supported' || info.status === 404;
}

function ResultList({ results }: { results: NotifyTestResult[] }) {
    const { t } = useT();

    return (
        <ul aria-label={t('notify_test_results')} className={'peb:flex peb:flex-col peb:gap-1 peb:text-sm'}>
            {results.map((r, i) => (
                <li key={`${r.type}-${r.target}-${i}`} className={'peb:flex peb:flex-wrap peb:items-center peb:gap-2'}>
                    <Badge tone={r.ok ? 'success' : 'danger'} variant={'soft'}>
                        {r.ok ? t('notify_test_ok') : t('notify_test_failed')}
                    </Badge>
                    <span>{t(`notify_type_${r.type}`)}</span>
                    <span className={'peb:font-mono peb:text-xs peb:break-all'}>{r.target}</span>
                    {!r.ok && r.error && <Muted>{r.error}</Muted>}
                </li>
            ))}
        </ul>
    );
}

/** Konfigurierte Benachrichtigungskanaele des Gateways (nur Anzeige) und Testnachricht. */
export default function NotificationsCard() {
    const { t } = useT();
    const query = useQuery({ queryKey: keys.notify(), queryFn: getNotifyConfig, retry: false, refetchOnWindowFocus: false });
    const test = useMutation({
        gcTime: 0,
        mutationFn: sendTestNotification,
        onError: (error) => {
            const info = errorInfo(error);
            toast.error(info.status === 429 ? t('notify_test_wait') : isNotifyUnsupported(error) ? t('notify_unsupported') : info.message || t('notify_test_error'));
        },
    });

    if (query.isError && isNotifyUnsupported(query.error)) {
        return (
            <Card title={t('notify_title')}>
                <p className={'peb:text-sm peb:text-muted-foreground'}>{t('notify_unsupported')}</p>
            </Card>
        );
    }

    return (
        <Card title={t('notify_title')}>
            <div className={'peb:flex peb:flex-col peb:gap-3'}>
                <p className={'peb:text-sm peb:text-muted-foreground'}>{t('notify_intro')}</p>
                <Loaded query={query}>
                    {(data) =>
                        data.channels.length === 0 ? (
                            <Alert type={'info'}>
                                <div className={'peb:flex peb:flex-col peb:gap-2 peb:text-sm'}>
                                    <p>{t('notify_none')}</p>
                                    <a href={NOTIFY_DOCS_URL} target={'_blank'} rel={'noreferrer'} className={'peb:text-accent peb:hover:underline'}>
                                        {t('notify_docs_link')}
                                    </a>
                                </div>
                            </Alert>
                        ) : (
                            <div className={'peb:flex peb:flex-col peb:gap-3'}>
                                <ul className={'peb:flex peb:flex-col peb:gap-1 peb:text-sm'}>
                                    {data.channels.map((c: NotifyChannel, i: number) => (
                                        <li key={`${c.type}-${c.target}-${i}`} className={'peb:flex peb:flex-wrap peb:items-center peb:gap-2'}>
                                            <Badge tone={'neutral'} variant={'soft'} className={'peb:bg-transparent peb:text-foreground'}>
                                                {t(`notify_type_${c.type}`)}
                                            </Badge>
                                            <span className={'peb:font-mono peb:text-xs peb:break-all'}>{c.target}</span>
                                            {c.format && <Muted>{`${t('notify_col_format')}: ${c.format}`}</Muted>}
                                        </li>
                                    ))}
                                </ul>
                                <p className={'peb:text-sm'}>
                                    <Muted>{`${t('notify_lang')}: `}</Muted>
                                    <span>{data.lang}</span>
                                </p>
                                <div>
                                    <Button color={'primary'} size={'small'} onClick={() => test.mutate()} disabled={test.isPending}>
                                        {test.isPending ? t('notify_testing') : t('notify_test')}
                                    </Button>
                                </div>
                                {test.data && <ResultList results={test.data.results} />}
                            </div>
                        )
                    }
                </Loaded>
            </div>
        </Card>
    );
}

import type { ReactNode } from 'react';
import type { UseQueryResult } from '@tanstack/react-query';
import { Alert, Button, Spinner } from '@pterodactyl/sdk';
import { errorInfo } from '../api';
import { useT, type MessageKey } from '../i18n';

const KNOWN_CODES = [
    'not_configured',
    'gateway_unreachable',
    'gateway_timeout',
    'gateway_unauthorized',
    'api_disabled',
    'gateway_invalid_response',
    'busy',
    'not_found',
    'not_complete',
    'network',
] as const;

/** Meldung zu einem Fehler der Extension-API: bekannter Code uebersetzt, sonst der Text des Servers. */
export function ErrorState({ error, onRetry }: { error: unknown; onRetry?: () => void }) {
    const { t } = useT();
    const info = errorInfo(error);
    const known = (KNOWN_CODES as readonly string[]).includes(info.code);
    const text = known ? t(`error_${info.code}` as MessageKey) : info.message || t('error_unknown');

    return (
        <Alert type={'danger'} title={t('error_title')}>
            <div className={'peb:flex peb:flex-col peb:gap-2'}>
                <p>{text}</p>
                {onRetry && (
                    <div>
                        <Button size={'xsmall'} isSecondary onClick={onRetry}>
                            {t('retry')}
                        </Button>
                    </div>
                )}
            </div>
        </Alert>
    );
}

/** Zeigt Ladeanzeige, Fehler oder den Inhalt einer Abfrage. */
export function Loaded<T>({ query, children }: { query: UseQueryResult<T>; children: (data: T) => ReactNode }) {
    if (query.data !== undefined) return <>{children(query.data)}</>;
    if (query.isError) return <ErrorState error={query.error} onRetry={() => void query.refetch()} />;

    return <Spinner centered />;
}

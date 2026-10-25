import { Badge } from '@pterodactyl/sdk';
import { formatBytes } from '../../format';
import { useT, type MessageKey } from '../../i18n';
import type { CheckStatus, SetupCheck } from '../../types';

const TONE = { ok: 'success', warn: 'warning', fail: 'danger' } as const;

export function StatusBadge({ status }: { status: CheckStatus }) {
    const { t } = useT();

    return (
        <Badge tone={TONE[status]} variant={'soft'}>
            {t(`setup_status_${status}` as MessageKey)}
        </Badge>
    );
}

/** Liste von Pruefungen mit Status und, bei warn/fail, dem Hinweis zur Behebung. */
export default function CheckList({ checks }: { checks: SetupCheck[] }) {
    const { t, locale } = useT();

    return (
        <ul className={'peb:flex peb:flex-col peb:divide-y peb:divide-border'}>
            {checks.map((check) => (
                <li key={check.id} data-status={check.status} className={'peb:flex peb:flex-col peb:gap-1 peb:py-2'}>
                    <div className={'peb:flex peb:flex-wrap peb:items-center peb:gap-2'}>
                        <StatusBadge status={check.status} />
                        <span className={'peb:font-medium'}>{t(`setup_check_${check.id}` as MessageKey)}</span>
                        {check.id === 'part_size' && check.value ? <span className={'peb:text-sm peb:text-muted-foreground'}>{formatBytes(check.value, locale)}</span> : null}
                    </div>
                    {check.status !== 'ok' && check.hint && <p className={'peb:text-sm peb:text-muted-foreground'}>{t(`setup_fix_${check.hint}` as MessageKey)}</p>}
                </li>
            ))}
        </ul>
    );
}

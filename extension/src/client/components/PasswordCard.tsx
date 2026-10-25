import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Alert, Button, toast } from '@pterodactyl/sdk';
import { errorInfo, getEmergencySheetData, getPasswordAck, keys, setPasswordAck } from '../api';
import { buildEmergencySheet } from '../emergencySheet';
import { formatDateTime } from '../format';
import { useT } from '../i18n';
import type { PasswordAck } from '../types';
import { Muted } from './Common';

export type PasswordState = 'hidden' | 'warn' | 'ok';

/**
 * Banner-Logik: Solange das Gateway keine Bestaetigung kennt, gilt die Warnung. Ist der Zustand
 * (noch) nicht lesbar, etwa bei einem Gateway vor 0.2.0, bleibt der Bereich verborgen.
 */
export function passwordState(ack: PasswordAck | undefined): PasswordState {
    if (!ack) return 'hidden';

    return ack.acknowledged ? 'ok' : 'warn';
}

/** Oeffnet das Notfallblatt in einem neuen Fenster. Nichts davon wird gespeichert. */
export function useEmergencySheet() {
    const { t, locale } = useT();

    return useMutation({
        mutationFn: async () => {
            // Fenster vor dem Netzwerkaufruf oeffnen, sonst blockiert der Browser es als Pop-up.
            const win = window.open('', '_blank');
            if (!win) throw new Error(t('sheet_popup_blocked'));
            try {
                const data = await getEmergencySheetData();
                const html = buildEmergencySheet(data, t, locale);
                win.document.open();
                win.document.write(html);
                win.document.close();
            } catch (error) {
                win.close();
                throw error;
            }
        },
        onError: (error) => {
            const info = errorInfo(error);
            toast.error(info.message || t('sheet_failed'));
        },
    });
}

/** Warnbanner fuer das Restic-Passwort; nach der Bestaetigung nur noch ein dezenter Hinweis. */
export default function PasswordCard() {
    const { t, locale } = useT();
    const queryClient = useQueryClient();
    const [busy, setBusy] = useState(false);
    const query = useQuery({ queryKey: keys.passwordAck(), queryFn: getPasswordAck, retry: false, refetchOnWindowFocus: false });
    const sheet = useEmergencySheet();
    const ack = useMutation({
        mutationFn: (value: boolean) => setPasswordAck(value),
        onMutate: () => setBusy(true),
        onSettled: () => setBusy(false),
        onSuccess: (data) => queryClient.setQueryData(keys.passwordAck(), data),
        onError: () => toast.error(t('password_ack_failed')),
    });
    const state = passwordState(query.data);
    if (state === 'hidden' || !query.data) return null;

    if (state === 'ok') {
        const ackData = query.data;
        const who = ackData.by ? t('password_ok_by', { name: ackData.by }) : '';
        const when = ackData.acknowledged_at ? t('password_ok_when', { date: formatDateTime(ackData.acknowledged_at, locale) }) : '';

        return (
            <div className={'peb:flex peb:flex-wrap peb:items-center peb:gap-3 peb:text-sm'}>
                <Muted>{t('password_ok_text', { who, when })}</Muted>
                <Button size={'xsmall'} isSecondary onClick={() => sheet.mutate()} disabled={sheet.isPending}>
                    {t('password_banner_sheet')}
                </Button>
                <Button size={'xsmall'} isSecondary onClick={() => ack.mutate(false)} disabled={busy}>
                    {t('password_ok_undo')}
                </Button>
            </div>
        );
    }

    return (
        <Alert type={'warning'} title={t('password_banner_title')}>
            <div className={'peb:flex peb:flex-col peb:gap-3'}>
                <p>{t('password_banner_text')}</p>
                <div className={'peb:flex peb:flex-wrap peb:gap-2'}>
                    <Button size={'small'} isSecondary onClick={() => sheet.mutate()} disabled={sheet.isPending}>
                        {t('password_banner_sheet')}
                    </Button>
                    <Button size={'small'} color={'primary'} onClick={() => ack.mutate(true)} disabled={busy}>
                        {t('password_banner_ack')}
                    </Button>
                </div>
            </div>
        </Alert>
    );
}

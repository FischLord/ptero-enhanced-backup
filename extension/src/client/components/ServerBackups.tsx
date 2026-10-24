import { useState } from 'react';
import { Button } from '@pterodactyl/sdk';
import { formatDateTime } from '../format';
import { useT } from '../i18n';
import type { BackupRow } from '../types';
import BackupsList from './BackupsList';
import FileBrowser from './FileBrowser';
import { Section } from './Common';
import ProHint from './ProHint';

/** Backups eines Servers; ein Klick auf "Browse files" oeffnet den Dateibrowser des Backups. */
export default function ServerBackups({ serverUuid }: { serverUuid: string }) {
    const { t, locale } = useT();
    const [backup, setBackup] = useState<BackupRow | null>(null);

    if (backup) {
        return (
            <Section
                title={t('files_of', { time: formatDateTime(backup.started_at, locale) })}
                actions={
                    <Button size={'small'} isSecondary onClick={() => setBackup(null)}>
                        {t('back_to_backups')}
                    </Button>
                }
            >
                <FileBrowser backupUuid={backup.backup_uuid} />
            </Section>
        );
    }

    return (
        <div className={'peb:flex peb:flex-col peb:gap-3'}>
            <ProHint text={'pro_retention_hint_server'} />
            <BackupsList serverUuid={serverUuid} onBrowse={setBackup} />
        </div>
    );
}

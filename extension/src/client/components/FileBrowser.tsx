import { useState } from 'react';
import type { MessageKey } from '../i18n';
import en from '../lang/en.json';
import { useQuery } from '@tanstack/react-query';
import { File, Folder, Link as LinkIcon } from 'lucide-react';
import { Alert, Button, Icon, Table, toast, type TableColumn } from '@pterodactyl/sdk';
import { FILES_PER_PAGE, errorInfo, fileDownloadUrl, getFiles, keys, probeDownload } from '../api';
import { formatBytes, formatDateTime } from '../format';
import { useT } from '../i18n';
import type { FileEntry } from '../types';
import { Loaded } from './Loaded';
import { Muted } from './Common';

const ICONS = { dir: Folder, file: File, symlink: LinkIcon, other: File } as const;

/** Verzeichnisansicht eines Backups aus dem Dateiindex des Gateways, mit Einzeldatei-Download. */
export default function FileBrowser({ backupUuid }: { backupUuid: string }) {
    const { t, locale } = useT();
    const [path, setPath] = useState('');
    const [page, setPage] = useState(1);
    const query = useQuery({ queryKey: keys.files(backupUuid, path, page), queryFn: () => getFiles(backupUuid, path, page), placeholderData: (previous) => previous, retry: false });
    const go = (next: string) => {
        setPath(next);
        setPage(1);
    };
    const [downloading, setDownloading] = useState<string | null>(null);
    // Erst pruefen, dann normal navigieren: Ein <a download> auf eine JSON-Route wuerde Fehlerantworten
    // (404, 409, 503, abgelaufene Sitzung) stillschweigend als Datei speichern. Grosse Dateien werden
    // so nie im Speicher des Browsers gepuffert.
    const download = async (entry: FileEntry) => {
        setDownloading(entry.path);
        try {
            await probeDownload(backupUuid, entry.path);
            window.location.assign(fileDownloadUrl(backupUuid, entry.path));
        } catch (error) {
            const info = errorInfo(error);
            const key = `error_${info.code}` as MessageKey;
            toast.error(`${entry.name}: ${key in en ? t(key) : info.status === 401 || info.status === 419 ? t('error_session') : info.message || t('error_unknown')}`);
        } finally {
            setDownloading(null);
        }
    };
    const parts = path === '' ? [] : path.split('/');

    const columns: TableColumn<FileEntry>[] = [
        {
            id: 'name',
            label: t('col_name'),
            cell: (e) => (
                <span className={'peb:flex peb:items-center peb:gap-2'}>
                    <Icon icon={ICONS[e.type]} className={'peb:text-muted-foreground'} />
                    {e.type === 'dir' ? (
                        <button type={'button'} className={'peb:cursor-pointer peb:font-medium peb:text-accent peb:hover:underline'} onClick={() => go(e.path)}>
                            {e.name}
                        </button>
                    ) : (
                        <span className={'peb:break-all'}>{e.name}</span>
                    )}
                </span>
            ),
        },
        { id: 'size', label: t('col_size'), cell: (e) => (e.type === 'dir' ? <Muted>–</Muted> : formatBytes(e.size, locale)) },
        { id: 'mtime', label: t('col_modified'), cell: (e) => formatDateTime(e.mtime, locale) },
        {
            id: 'actions',
            label: '',
            cell: (e) =>
                e.type === 'file' ? (
                    <Button size={'xsmall'} isSecondary isLoading={downloading === e.path} onClick={() => void download(e)}>
                        {t('download')}
                    </Button>
                ) : null,
        },
    ];

    return (
        <div className={'peb:flex peb:flex-col peb:gap-3'}>
            <nav aria-label={t('breadcrumb')} className={'peb:flex peb:flex-wrap peb:items-center peb:gap-1 peb:text-sm'}>
                <button type={'button'} className={'peb:cursor-pointer peb:text-accent peb:hover:underline'} onClick={() => go('')}>
                    {t('root')}
                </button>
                {parts.map((part, i) => (
                    <span key={i} className={'peb:flex peb:items-center peb:gap-1'}>
                        <span className={'peb:text-muted-foreground'}>/</span>
                        {i === parts.length - 1 ? (
                            <span className={'peb:font-medium peb:text-foreground'}>{part}</span>
                        ) : (
                            <button type={'button'} className={'peb:cursor-pointer peb:text-accent peb:hover:underline'} onClick={() => go(parts.slice(0, i + 1).join('/'))}>
                                {part}
                            </button>
                        )}
                    </span>
                ))}
            </nav>
            <Loaded query={query}>
                {(data) => (
                    <>
                        {data.index_truncated && <Alert type={'warning'}>{t('index_truncated')}</Alert>}
                        <Table
                            rows={data.items}
                            columns={columns}
                            keyOf={(e) => e.path}
                            isFetching={query.isFetching}
                            emptyState={t('files_empty')}
                            pagination={{
                                page,
                                perPage: FILES_PER_PAGE,
                                total: data.total,
                                count: data.items.length,
                                itemLabel: t('files_label'),
                                onPageSelect: setPage,
                            }}
                        />
                        <p className={'peb:text-xs peb:text-muted-foreground'}>{t('download_hint')}</p>
                    </>
                )}
            </Loaded>
        </div>
    );
}

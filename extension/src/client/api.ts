import { http } from '@pterodactyl/sdk';
import type {
    ApiErrorInfo,
    BackupRow,
    Edition,
    FilesResponse,
    MaintenanceJob,
    MaintenanceJobName,
    Overview,
    Paged,
    ServersResponse,
} from './types';

export const EXTENSION_ID = 'peb-backups';
export const BASE = `/api/admin/extensions/${EXTENSION_ID}`;

export const keys = {
    overview: (days: number) => [EXTENSION_ID, 'overview', days] as const,
    servers: () => [EXTENSION_ID, 'servers'] as const,
    backups: (server: string | null, page: number) => [EXTENSION_ID, 'backups', server, page] as const,
    files: (backup: string, path: string, page: number) => [EXTENSION_ID, 'files', backup, path, page] as const,
    maintenance: () => [EXTENSION_ID, 'maintenance'] as const,
    edition: () => [EXTENSION_ID, 'edition'] as const,
};

export const BACKUPS_PER_PAGE = 25;
export const FILES_PER_PAGE = 100;

export async function getOverview(days = 30): Promise<Overview> {
    return (await http.get<Overview>(`${BASE}/overview`, { params: { days } })).data;
}

export async function getEdition(): Promise<Edition> {
    return (await http.get<Edition>(`${BASE}/edition`)).data;
}

export async function getServers(): Promise<ServersResponse> {
    return (await http.get<ServersResponse>(`${BASE}/servers`)).data;
}

export async function getBackups(server: string | null, page: number): Promise<Paged<BackupRow>> {
    const params: Record<string, string | number> = { limit: BACKUPS_PER_PAGE, offset: (page - 1) * BACKUPS_PER_PAGE };
    if (server) params.server = server;

    return (await http.get<Paged<BackupRow>>(`${BASE}/backups`, { params })).data;
}

export async function getFiles(backup: string, path: string, page: number): Promise<FilesResponse> {
    const params = { path, limit: FILES_PER_PAGE, offset: (page - 1) * FILES_PER_PAGE };

    return (await http.get<FilesResponse>(`${BASE}/backups/${backup}/files`, { params })).data;
}

/** Der Browser laedt die Datei ueber die Panel-Sitzung; das Panel streamt sie vom Gateway durch. */
export function fileDownloadUrl(backup: string, path: string): string {
    return `${BASE}/backups/${backup}/file?path=${encodeURIComponent(path)}`;
}

/** Prueft vor dem Download (Sitzung, Backup fertig, Datei vorhanden); wirft bei Fehlern. */
export async function probeDownload(backup: string, path: string): Promise<void> {
    await http.get(`${BASE}/backups/${backup}/file`, { params: { path, probe: 1 } });
}

export async function getMaintenance(limit = 50): Promise<{ items: MaintenanceJob[] }> {
    return (await http.get<{ items: MaintenanceJob[] }>(`${BASE}/maintenance`, { params: { limit } })).data;
}

export async function runMaintenance(jobs: MaintenanceJobName[]): Promise<{ id: number; job_ids: number[] }> {
    return (await http.post<{ id: number; job_ids: number[] }>(`${BASE}/maintenance/run`, { jobs, confirm: true })).data;
}

/** Liest {error, message} aus einer Fehlerantwort; faellt auf eine allgemeine Meldung zurueck. */
export function errorInfo(error: unknown): ApiErrorInfo {
    const response = (error as { response?: { status?: number; data?: { error?: unknown; message?: unknown } } } | null)?.response;
    const data = response?.data;
    const code = typeof data?.error === 'string' ? data.error : response ? `http_${response.status ?? 0}` : 'network';
    const message = typeof data?.message === 'string' ? data.message : error instanceof Error ? error.message : '';

    return { code, message, status: response?.status ?? null };
}

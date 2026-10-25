import { http } from '@pterodactyl/sdk';
import type {
    ApiErrorInfo,
    BackupRow,
    CreatedGatewayServer,
    GatewayServerForm,
    SelfTestResult,
    SetupAllocation,
    SetupNode,
    SetupState,
    SetupUser,
    TrashResponse,
    Edition,
    CreatedKey,
    EmergencySheetData,
    PasswordAck,
    RepoKey,
    FilesResponse,
    MaintenanceJob,
    RunJobName,
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
    setup: () => [EXTENSION_ID, 'setup'] as const,
    setupNodes: () => [EXTENSION_ID, 'setup', 'nodes'] as const,
    setupAllocations: (node: number) => [EXTENSION_ID, 'setup', 'allocations', node] as const,
    passwordAck: () => [EXTENSION_ID, 'password-ack'] as const,
    repoKeys: () => [EXTENSION_ID, 'keys'] as const,
    setupUsers: (q: string) => [EXTENSION_ID, 'setup', 'users', q] as const,
    trash: () => [EXTENSION_ID, 'trash'] as const,
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

export async function runMaintenance(jobs: RunJobName[]): Promise<{ id: number; job_ids: number[] }> {
    return (await http.post<{ id: number; job_ids: number[] }>(`${BASE}/maintenance/run`, { jobs, confirm: true })).data;
}

export async function getSetup(): Promise<SetupState> {
    return (await http.get<SetupState>(`${BASE}/setup`)).data;
}

export async function importEgg(mode: 'import' | 'update'): Promise<{ egg_id: number; action: string }> {
    return (await http.post<{ egg_id: number; action: string }>(`${BASE}/setup/egg`, { mode })).data;
}

export async function getSetupNodes(): Promise<{ items: SetupNode[] }> {
    return (await http.get<{ items: SetupNode[] }>(`${BASE}/setup/nodes`)).data;
}

export async function getSetupAllocations(node: number): Promise<{ items: SetupAllocation[] }> {
    return (await http.get<{ items: SetupAllocation[] }>(`${BASE}/setup/nodes/${node}/allocations`)).data;
}

export async function searchSetupUsers(q: string): Promise<{ items: SetupUser[] }> {
    return (await http.get<{ items: SetupUser[] }>(`${BASE}/setup/users`, { params: { q } })).data;
}

export async function createGatewayServer(form: GatewayServerForm): Promise<CreatedGatewayServer> {
    return (await http.post<CreatedGatewayServer>(`${BASE}/setup/server`, form)).data;
}

export async function runSelfTest(): Promise<SelfTestResult> {
    return (await http.post<SelfTestResult>(`${BASE}/setup/self-test`)).data;
}

export async function getPasswordAck(): Promise<PasswordAck> {
    return (await http.get<PasswordAck>(`${BASE}/password-ack`)).data;
}

export async function setPasswordAck(acknowledged: boolean): Promise<PasswordAck> {
    return (await http.put<PasswordAck>(`${BASE}/password-ack`, { acknowledged })).data;
}

export async function getEmergencySheetData(): Promise<EmergencySheetData> {
    return (await http.get<EmergencySheetData>(`${BASE}/emergency-sheet`)).data;
}

export async function getRepoKeys(): Promise<{ keys: RepoKey[] }> {
    return (await http.get<{ keys: RepoKey[] }>(`${BASE}/keys`)).data;
}

export async function createRepoKey(label: string): Promise<CreatedKey> {
    return (await http.post<CreatedKey>(`${BASE}/keys`, { label })).data;
}

export async function deleteRepoKey(id: string): Promise<void> {
    await http.delete(`${BASE}/keys/${id}`);
}

export async function getTrash(): Promise<TrashResponse> {
    return (await http.get<TrashResponse>(`${BASE}/trash`)).data;
}

/** Macht das Backup wieder zu einem normalen Backup des Gateways (das Panel kennt es danach trotzdem nicht). */
export async function restoreFromTrash(backup: string): Promise<{ backup_uuid: string }> {
    return (await http.post<{ backup_uuid: string }>(`${BASE}/trash/${backup}/restore`)).data;
}

/** Loescht ein Backup sofort und endgueltig. */
export async function purgeFromTrash(backup: string): Promise<{ backup_uuid: string }> {
    return (await http.delete<{ backup_uuid: string }>(`${BASE}/trash/${backup}`, { data: { confirm: true } })).data;
}

/** Liest {error, message} aus einer Fehlerantwort; faellt auf eine allgemeine Meldung zurueck. */
export function errorInfo(error: unknown): ApiErrorInfo {
    const response = (error as { response?: { status?: number; data?: { error?: unknown; message?: unknown } } } | null)?.response;
    const data = response?.data;
    const code = typeof data?.error === 'string' ? data.error : response ? `http_${response.status ?? 0}` : 'network';
    const message = typeof data?.message === 'string' ? data.message : error instanceof Error ? error.message : '';

    return { code, message, status: response?.status ?? null };
}

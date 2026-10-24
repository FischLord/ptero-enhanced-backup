import { afterEach, beforeEach, expect, test, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { createExtensionTestHost } from '@pterodactyl/sdk/testing';
import * as api from '../../src/client/api';
import { BACKUP_A, backup, files, job, overview, servers } from './fixtures';
import BackupsScreen from '../../src/client/screens/BackupsScreen';

vi.mock('../../src/client/api', async (importOriginal) => ({
    ...(await importOriginal<typeof import('../../src/client/api')>()),
    getOverview: vi.fn(),
    getServers: vi.fn(),
    getBackups: vi.fn(),
    getFiles: vi.fn(),
    getMaintenance: vi.fn(),
    runMaintenance: vi.fn(),
    probeDownload: vi.fn(),
}));

const mocked = vi.mocked(api);
let host: ReturnType<typeof createExtensionTestHost> | undefined;

beforeEach(() => {
    host = createExtensionTestHost({ extensionId: 'peb-backups', prefix: 'peb', path: '/panel/backups' });
    mocked.getOverview.mockResolvedValue(overview());
    mocked.getServers.mockResolvedValue(servers());
    mocked.getBackups.mockResolvedValue({ items: [backup()], total: 1, limit: 25, offset: 0 });
    mocked.getFiles.mockResolvedValue(files());
    mocked.getMaintenance.mockResolvedValue({ items: [job(), job({ id: 2, type: 'check', result: 'skipped', message: 'skipped: busy', bytes_freed: null })] });
});

afterEach(() => {
    cleanup();
    host?.dispose();
    host = undefined;
    vi.clearAllMocks();
});

const renderScreen = () => render(<BackupsScreen />, { wrapper: host!.Wrapper });

test('overview shows savings, health and the forecast from the gateway data', async () => {
    renderScreen();

    expect(await screen.findByText('Without PEB (logical)')).toBeTruthy();
    expect(screen.getByText('9.97 GB')).toBeTruthy();
    expect(screen.getByText('Actually stored (on disk)')).toBeTruthy();
    expect(screen.getAllByText('1.76 GB').length).toBeGreaterThan(0);
    expect(screen.getByText(/5\.7×/)).toBeTruthy();
    expect(screen.getByText('Saved per month')).toBeTruthy();
    expect(screen.getByText('no errors found')).toBeTruthy();
    expect(screen.getByText(/1 running, 2 queued/)).toBeTruthy();
    expect(screen.getByText(/Full in about 1,447 days/)).toBeTruthy();
    expect(screen.getByRole('img', { name: 'Repository size (stored)' })).toBeTruthy();
});

test('storage target card shows free space and the estimates', async () => {
    renderScreen();

    expect(await screen.findByText('Storage target')).toBeTruthy();
    expect(screen.getByText(/Full in ~4,083 days/)).toBeTruthy();
    expect(screen.getByText(/Room for ~1,816 more backups/)).toBeTruthy();
    expect(screen.getByText(/3\.1 TB free, 22/)).toBeTruthy();
    expect(screen.queryByText(/est\./)).toBeNull();
});

test('storage target card marks estimates and handles missing data', async () => {
    const o = overview();
    o.summary.target = { ...o.summary.target!, exact: false, days_until_full: null, backups_remaining: null };
    mocked.getOverview.mockResolvedValue(o);
    renderScreen();

    expect(await screen.findByText(/not enough growth data/)).toBeTruthy();
    expect(screen.getByText(/no recent backups/)).toBeTruthy();
    expect(screen.getAllByText(/\(est\.\)/).length).toBeGreaterThan(0);
});

test('storage target card explains when the free space is unknown', async () => {
    const o = overview();
    o.summary.target = null;
    mocked.getOverview.mockResolvedValue(o);
    renderScreen();

    expect(await screen.findByText(/Free space on the backup target is not known yet/)).toBeTruthy();
});

test('marks the stored size as approximate when it is not measured on disk', async () => {
    const o = overview();
    o.summary.stored_bytes_exact = false;
    mocked.getOverview.mockResolvedValue(o);
    renderScreen();

    expect(await screen.findByText('Actually stored (approx.)')).toBeTruthy();
    expect(screen.getByText('Saved per month (approx.)')).toBeTruthy();
});

test('shows a clear message when the gateway is unreachable and can retry', async () => {
    mocked.getOverview.mockRejectedValueOnce({ response: { status: 502, data: { error: 'gateway_unreachable', message: 'The gateway is not reachable from the panel server.' } } });
    renderScreen();

    expect(await screen.findByText(/not reachable from the panel server/)).toBeTruthy();
    mocked.getOverview.mockResolvedValue(overview());
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }));
    expect(await screen.findByText('Without PEB (logical)')).toBeTruthy();
});

test('explains a disabled admin API', async () => {
    mocked.getOverview.mockRejectedValue({ response: { status: 502, data: { error: 'api_disabled', message: 'x' } } });
    renderScreen();

    expect(await screen.findByText(/admin API is switched off on the gateway/)).toBeTruthy();
});

test('server table flags failed servers, labels unknown servers and opens the backups', async () => {
    renderScreen();
    fireEvent.click(await screen.findByRole('tab', { name: 'Servers' }));

    expect(await screen.findByText('Satisfactory 2024')).toBeTruthy();
    expect(screen.getByText('Unknown server')).toBeTruthy();
    expect(screen.getByText('Last backup failed')).toBeTruthy();
    expect(screen.getByText(/1 server\(s\) need attention/)).toBeTruthy();
    expect(screen.getByText('Data added')).toBeTruthy();
    expect(screen.getByText('4.85 GB')).toBeTruthy(); // size of the last backup
    expect(screen.getByText('121 MB')).toBeTruthy(); // data added by the last backup

    const row = screen.getByText('Satisfactory 2024').closest('tr')!;
    fireEvent.click(within(row).getByRole('button', { name: 'Backups' }));
    expect(await screen.findByText('Backups of Satisfactory 2024')).toBeTruthy();
    expect(mocked.getBackups).toHaveBeenCalledWith('2b7fdd72-690d-4ffa-8c1e-5c299ab53da5', 1);
});

test('file browser lists the index and offers single-file downloads through the panel', async () => {
    renderScreen();
    fireEvent.click(await screen.findByRole('tab', { name: 'Servers' }));
    const row = (await screen.findByText('Satisfactory 2024')).closest('tr')!;
    fireEvent.click(within(row).getByRole('button', { name: 'Backups' }));
    fireEvent.click(await screen.findByRole('button', { name: 'Browse files' }));

    expect(await screen.findByText('server.properties')).toBeTruthy();
    // Download: erst Probe, dann Navigation; ein Fehler der Probe wird gemeldet statt als Datei gespeichert.
    mocked.probeDownload.mockRejectedValueOnce({ response: { status: 404, data: { error: 'not_found', message: 'x' } } });
    fireEvent.click(screen.getByRole('button', { name: 'Download' }));
    await waitFor(() => expect(mocked.probeDownload).toHaveBeenCalledWith(BACKUP_A, 'server.properties'));
    expect(api.fileDownloadUrl(BACKUP_A, 'server.properties')).toBe(`/api/admin/extensions/peb-backups/backups/${BACKUP_A}/file?path=server.properties`);

    fireEvent.click(screen.getByRole('button', { name: 'world' }));
    await waitFor(() => expect(mocked.getFiles).toHaveBeenCalledWith(BACKUP_A, 'world', 1));
});

test('maintenance needs the confirmation dialog before anything runs', async () => {
    mocked.runMaintenance.mockResolvedValue({ id: 15, job_ids: [15, 16] });
    renderScreen();
    fireEvent.click(await screen.findByRole('tab', { name: 'Maintenance' }));

    expect(await screen.findByText('skipped: busy')).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: 'Run now' }));
    expect(mocked.runMaintenance).not.toHaveBeenCalled();

    fireEvent.click(await screen.findByRole('checkbox', { name: /Statistics/ }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.click(within(dialog).getByRole('button', { name: 'Run now' }));
    await waitFor(() => expect(mocked.runMaintenance).toHaveBeenCalledWith(['prune', 'check']));
});

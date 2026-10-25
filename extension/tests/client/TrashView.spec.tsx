import { afterEach, beforeEach, expect, test, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { createExtensionTestHost } from '@pterodactyl/sdk/testing';
import * as api from '../../src/client/api';
import { BACKUP_A, SERVER_A, SERVER_B, overview } from './fixtures';
import BackupsScreen from '../../src/client/screens/BackupsScreen';
import type { Overview, TrashResponse, TrashRow } from '../../src/client/types';
import en from '../../src/client/lang/en.json';
import de from '../../src/client/lang/de.json';

vi.mock('../../src/client/api', async (importOriginal) => ({
    ...(await importOriginal<typeof import('../../src/client/api')>()),
    getOverview: vi.fn(),
    getTrash: vi.fn(),
    restoreFromTrash: vi.fn(),
    purgeFromTrash: vi.fn(),
}));

const mocked = vi.mocked(api);
let host: ReturnType<typeof createExtensionTestHost> | undefined;

const row = (over: Partial<TrashRow> = {}): TrashRow => ({
    backup_uuid: BACKUP_A,
    server_uuid: SERVER_A,
    server: { id: 7, name: 'Survival', node: 'wings-1', owner: 'alex', backup_limit: 5, backup_count: 2 },
    reason: 'panel_delete',
    logical_bytes: 4849859072,
    received_bytes: 4850275362,
    finished_at: '2026-10-20T14:09:33Z',
    trashed_at: '2026-10-24T10:00:00Z',
    purge_after: '2026-10-31T10:00:00Z',
    ...over,
});

const trash = (items: TrashRow[]): TrashResponse => ({ total: items.length, logical_bytes: items.reduce((n, r) => n + r.logical_bytes, 0), items });

const withTrash = (count: number): Overview => {
    const base = overview();
    return { ...base, summary: { ...base.summary, trash: { count, logical_bytes: 1, next_purge_at: '2026-10-31T10:00:00Z' } } };
};

beforeEach(() => {
    host = createExtensionTestHost({ extensionId: 'peb-backups', prefix: 'peb', path: '/panel/backups' });
    mocked.getOverview.mockResolvedValue(withTrash(2));
    mocked.getTrash.mockResolvedValue(
        trash([row(), row({ backup_uuid: 'e1c0a3c4-2c1f-4f5b-9d0e-5f4f4a7a2b11', server_uuid: SERVER_B, server: null, reason: 'server_deleted', logical_bytes: 1000000 })]),
    );
});

afterEach(() => {
    cleanup();
    host?.dispose();
    host = undefined;
    vi.clearAllMocks();
});

const renderScreen = () => render(<BackupsScreen />, { wrapper: host!.Wrapper });
const openTrash = async () => fireEvent.click(await screen.findByRole('tab', { name: /^Trash/ }));

test('the trash tab shows the number of trashed backups', async () => {
    renderScreen();

    const tab = await screen.findByRole('tab', { name: /^Trash/ });
    await waitFor(() => expect(within(tab).getByText('2')).toBeTruthy());
});

test('no badge while the trash is empty or the gateway does not know a trash', async () => {
    mocked.getOverview.mockResolvedValue(overview());
    renderScreen();

    const tab = await screen.findByRole('tab', { name: /^Trash/ });
    await screen.findByText('Without PEB (logical)');
    expect(within(tab).queryByText(/^\d+$/)).toBeNull();
});

test('the trash table lists server, backup, size, deletion and purge dates', async () => {
    renderScreen();
    await openTrash();

    expect(await screen.findByText('Survival')).toBeTruthy();
    expect(screen.getByText(BACKUP_A)).toBeTruthy();
    expect(screen.getByText('Deleted in the panel')).toBeTruthy();
    expect(screen.getByText('Server deleted')).toBeTruthy();
    expect(screen.getByText('4.85 GB')).toBeTruthy();
    expect(screen.getByText('Unknown server')).toBeTruthy();
    expect(screen.getAllByRole('button', { name: 'Restore' })).toHaveLength(2);
});

test('restore calls the API right away', async () => {
    mocked.restoreFromTrash.mockResolvedValue({ backup_uuid: BACKUP_A });
    renderScreen();
    await openTrash();

    fireEvent.click((await screen.findAllByRole('button', { name: 'Restore' }))[0]);
    await waitFor(() => expect(mocked.restoreFromTrash).toHaveBeenCalledWith(BACKUP_A));
});

test('deleting for good needs the confirmation dialog', async () => {
    mocked.purgeFromTrash.mockResolvedValue({ backup_uuid: BACKUP_A });
    renderScreen();
    await openTrash();

    fireEvent.click((await screen.findAllByRole('button', { name: 'Delete now' }))[0]);
    expect(mocked.purgeFromTrash).not.toHaveBeenCalled();
    const dialog = await screen.findByRole('dialog');
    fireEvent.click(within(dialog).getByRole('button', { name: 'Delete for good' }));
    await waitFor(() => expect(mocked.purgeFromTrash).toHaveBeenCalledWith(BACKUP_A));
});

test('an empty trash shows the empty state', async () => {
    mocked.getTrash.mockResolvedValue(trash([]));
    renderScreen();
    await openTrash();

    expect(await screen.findByText('The trash is empty.')).toBeTruthy();
});

test('the trash texts exist in German with the same placeholders', () => {
    const keys = Object.keys(en).filter((k) => k.startsWith('trash_') || k === 'tab_trash' || k === 'job_trash_purge');
    expect(keys.length).toBeGreaterThan(10);
    for (const key of keys) {
        const german = (de as Record<string, string>)[key];
        expect(german, key).toBeTruthy();
        const placeholders = (text: string) => (text.match(/\{\{\s*\w+\s*\}\}/g) ?? []).sort();
        expect(placeholders(german)).toEqual(placeholders((en as Record<string, string>)[key]));
    }
});

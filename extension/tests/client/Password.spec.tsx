import { afterEach, beforeEach, expect, test, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { createExtensionTestHost } from '@pterodactyl/sdk/testing';
import * as api from '../../src/client/api';
import PasswordCard, { passwordState } from '../../src/client/components/PasswordCard';
import EmergencyKeysCard, { CreatedKeyPanel } from '../../src/client/components/EmergencyKeysCard';
import { buildEmergencySheet, repoAddress } from '../../src/client/emergencySheet';
import en from '../../src/client/lang/en.json';
import de from '../../src/client/lang/de.json';
import type { CreatedKey, EmergencySheetData, RepoKey } from '../../src/client/types';

vi.mock('../../src/client/api', async (importOriginal) => ({
    ...(await importOriginal<typeof import('../../src/client/api')>()),
    getPasswordAck: vi.fn(),
    setPasswordAck: vi.fn(),
    getEmergencySheetData: vi.fn(),
    getRepoKeys: vi.fn(),
    createRepoKey: vi.fn(),
    deleteRepoKey: vi.fn(),
}));

const mocked = vi.mocked(api);
let host: ReturnType<typeof createExtensionTestHost> | undefined;

const SECRET = 'S3cretPassw0rdThatMustNeverAppear';
const UUID = '2b7fdd72-690d-4ffa-8c1e-5c299ab53da5';

const t = (key: keyof typeof en, values?: Record<string, string | number>) =>
    en[key].replace(/\{\{\s*(\w+)\s*\}\}/g, (_, n: string) => String(values?.[n] ?? ''));

const sheetData = (over: Partial<EmergencySheetData> = {}): EmergencySheetData => ({
    gateway_url: 'http://gw.example:9000',
    repo: { type: 'rest', location: 'https://nas.example:8000/peb' },
    secondary: null,
    servers: [
        { uuid: UUID, name: 'Survival <1>' },
        { uuid: '457727f5-332e-43b8-af62-37e7baecbcf9', name: null },
    ],
    generated_at: '2026-10-24T15:00:00Z',
    ...over,
});

const repoKey = (over: Partial<RepoKey> = {}): RepoKey => ({
    id: 'a'.repeat(64),
    short_id: 'aaaaaaaa',
    created_at: '2026-10-20T10:00:00Z',
    host: 'gateway',
    user: 'root',
    current: true,
    emergency: false,
    label: '',
    ...over,
});

beforeEach(() => {
    host = createExtensionTestHost({ extensionId: 'peb-backups', prefix: 'peb', path: '/panel/backups' });
});

afterEach(() => {
    cleanup();
    host?.dispose();
    host = undefined;
    vi.clearAllMocks();
});

test('banner state: hidden without data, warning until acknowledged, then ok', () => {
    expect(passwordState(undefined)).toBe('hidden');
    expect(passwordState({ acknowledged: false, acknowledged_at: null, by: '' })).toBe('warn');
    expect(passwordState({ acknowledged: true, acknowledged_at: '2026-10-20T10:00:00Z', by: 'root' })).toBe('ok');
});

test('shows the warning banner while not acknowledged, and a quiet note after the button', async () => {
    mocked.getPasswordAck.mockResolvedValue({ acknowledged: false, acknowledged_at: null, by: '' });
    mocked.setPasswordAck.mockResolvedValue({ acknowledged: true, acknowledged_at: '2026-10-24T15:00:00Z', by: 'root' });
    render(<PasswordCard />, { wrapper: host!.Wrapper });

    expect(await screen.findByText('Is your restic password stored in a second place?')).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: 'I have stored the password in a second place' }));
    await waitFor(() => expect(mocked.setPasswordAck).toHaveBeenCalledWith(true));
    expect(await screen.findByText(/confirmed as stored in a second place by root/)).toBeTruthy();
    expect(screen.queryByText('Is your restic password stored in a second place?')).toBeNull();
});

test('shows only the quiet note when already acknowledged', async () => {
    mocked.getPasswordAck.mockResolvedValue({ acknowledged: true, acknowledged_at: '2026-10-20T10:00:00Z', by: 'root' });
    render(<PasswordCard />, { wrapper: host!.Wrapper });

    expect(await screen.findByText(/confirmed as stored in a second place/)).toBeTruthy();
    expect(screen.queryByText('Is your restic password stored in a second place?')).toBeNull();
});

test('shows nothing when the gateway cannot tell (older gateway)', async () => {
    mocked.getPasswordAck.mockRejectedValue(new Error('old gateway'));
    const { container } = render(<PasswordCard />, { wrapper: host!.Wrapper });

    await waitFor(() => expect(mocked.getPasswordAck).toHaveBeenCalled());
    expect(container.textContent).not.toContain('restic password');
});

test('emergency sheet contains repository, offsite, gateway, restore commands and servers but no password', () => {
    const html = buildEmergencySheet(sheetData({ secondary: { type: 'rclone', repo: 'rclone:gdrive:peb' } }), t, 'en');

    expect(html).toContain('rest:https://nas.example:8000/peb');
    expect(html).toContain('rclone:gdrive:peb');
    expect(html).toContain('rclone.conf');
    expect(html).toContain('http://gw.example:9000');
    expect(html).toContain(`restic -r rest:https://nas.example:8000/peb snapshots --tag server:${UUID}`);
    expect(html).toContain(`dump &lt;snapshot&gt; /${UUID}.tar &gt; ${UUID}.tar`);
    expect(html).not.toContain('&gt; backup.tar');
    expect(html).toContain('RESTIC_PASSWORD=...');
    expect(html).toContain('Survival &lt;1&gt;');
    expect(html).not.toContain('Survival <1>');
    expect(html).toContain('(unknown in the panel)');
    expect(html).toContain('<div class="box"></div>');
    expect(html).not.toContain(SECRET);
});

test('emergency sheet works for local repositories, without offsite, and in German', () => {
    const tde = (key: keyof typeof de) => de[key];
    const html = buildEmergencySheet(sheetData({ repo: { type: 'local', location: '/mnt/peb/repo' }, servers: [] }), tde, 'de');

    expect(html).toContain('<html lang="de">');
    expect(html).toContain('restic -r /mnt/peb/repo snapshots');
    expect(html).toContain(de.sheet_offsite_none);
    expect(html).toContain(de.sheet_servers_empty);
    expect(html).not.toContain('rclone.conf');
    expect(repoAddress('sftp', 'host:/path')).toBe('sftp:host:/path');
    expect(repoAddress('rclone', 'rclone:gdrive:peb')).toBe('rclone:gdrive:peb');
});

test('created key password is shown once and gone after confirming', async () => {
    const created: CreatedKey = { id: 'b'.repeat(64), short_id: 'bbbbbbbb', label: 'hoster', host: 'hoster', user: 'peb-emergency', created_at: null, password: SECRET };
    mocked.getRepoKeys.mockResolvedValue({ keys: [repoKey()] });
    mocked.createRepoKey.mockResolvedValue(created);
    render(<EmergencyKeysCard />, { wrapper: host!.Wrapper });

    await screen.findByText('aaaaaaaa');
    fireEvent.change(screen.getByLabelText('Label'), { target: { value: 'hoster' } });
    fireEvent.click(screen.getByRole('button', { name: 'Create second key' }));
    await waitFor(() => expect(mocked.createRepoKey).toHaveBeenCalledWith('hoster'));
    expect(await screen.findByText(SECRET)).toBeTruthy();
    expect(screen.getByText(/shown only once/)).toBeTruthy();

    fireEvent.click(screen.getByRole('button', { name: 'I have stored it' }));
    expect(screen.queryByText(SECRET)).toBeNull();
    expect(document.body.textContent).not.toContain(SECRET);
});

test('CreatedKeyPanel shows the password with a copy button', async () => {
    render(<CreatedKeyPanel created={{ id: 'b', short_id: 'bbbbbbbb', label: 'x', host: 'x', user: 'u', created_at: null, password: SECRET }} onDone={() => undefined} />, {
        wrapper: host!.Wrapper,
    });
    expect(await screen.findByText(SECRET)).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Copy' })).toBeTruthy();
});

test('lists keys, offers delete only for other keys and asks for confirmation', async () => {
    mocked.getRepoKeys.mockResolvedValue({ keys: [repoKey(), repoKey({ id: 'c'.repeat(64), short_id: 'cccccccc', current: false, emergency: true, label: 'hoster', host: 'hoster' })] });
    mocked.deleteRepoKey.mockResolvedValue(undefined);
    render(<EmergencyKeysCard />, { wrapper: host!.Wrapper });

    await screen.findByText('cccccccc');
    expect(screen.getByText('In use by the gateway')).toBeTruthy();
    const deletes = screen.getAllByRole('button', { name: 'Delete' });
    expect(deletes).toHaveLength(1);
    fireEvent.click(deletes[0]);
    expect(mocked.deleteRepoKey).not.toHaveBeenCalled();

    const dialog = await screen.findByRole('dialog');
    fireEvent.click(within(dialog).getByRole('button', { name: 'Delete key' }));
    await waitFor(() => expect(mocked.deleteRepoKey).toHaveBeenCalledWith('c'.repeat(64)));
});

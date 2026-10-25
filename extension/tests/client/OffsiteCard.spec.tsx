import { afterEach, beforeEach, expect, test, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { createExtensionTestHost } from '@pterodactyl/sdk/testing';
import * as api from '../../src/client/api';
import { job, overview, servers } from './fixtures';
import BackupsScreen from '../../src/client/screens/BackupsScreen';
import OffsiteCard, { offsiteWarning, pruneSkipWarning } from '../../src/client/components/OffsiteCard';
import type { SecondaryStatus } from '../../src/client/types';
import en from '../../src/client/lang/en.json';
import de from '../../src/client/lang/de.json';

vi.mock('../../src/client/api', async (importOriginal) => ({
    ...(await importOriginal<typeof import('../../src/client/api')>()),
    getOverview: vi.fn(),
    getServers: vi.fn(),
    getMaintenance: vi.fn(),
    runMaintenance: vi.fn(),
}));

const mocked = vi.mocked(api);
let host: ReturnType<typeof createExtensionTestHost> | undefined;
const NOW = new Date('2026-10-24T15:00:00Z').getTime();

const secondary = (over: Partial<SecondaryStatus> = {}): SecondaryStatus => ({
    configured: true,
    repo: 'rclone:gdrive:peb',
    type: 'rclone',
    state: 'ok',
    running: false,
    last_copy_at: '2026-10-24T14:30:00Z',
    last_copy_result: 'ok',
    last_error: '',
    lag_snapshots: 0,
    lag_since: null,
    stored_bytes: 1500000000,
    stored_at: '2026-10-24T02:10:00Z',
    next_copy_at: '2026-10-25T01:00:00Z',
    ...over,
});

const withSecondary = (s?: SecondaryStatus) => {
    const o = overview();
    if (s) o.summary.secondary = s;

    return o;
};

beforeEach(() => {
    host = createExtensionTestHost({ extensionId: 'peb-backups', prefix: 'peb', path: '/panel/backups' });
});

afterEach(() => {
    cleanup();
    host?.dispose();
    host = undefined;
    vi.clearAllMocks();
});

const renderCard = (s?: SecondaryStatus) => render(<OffsiteCard overview={withSecondary(s)} now={NOW} />, { wrapper: host!.Wrapper });

test('shows a note and a docs link when no offsite copy is configured', async () => {
    renderCard(undefined);
    await screen.findByText('Offsite copy');

    expect(screen.getByText('No offsite copy configured.')).toBeTruthy();
    expect(screen.getByRole('link', { name: 'How to set it up' }).getAttribute('href')).toContain('#offsite-copy');
    cleanup();
    renderCard(secondary({ configured: false }));
    await screen.findByText('Offsite copy');
    expect(screen.getByText('No offsite copy configured.')).toBeTruthy();
});

test('shows status, last copy, lag, used space and target of a healthy offsite copy', async () => {
    renderCard(secondary());
    await screen.findByText('Offsite copy');

    expect(screen.getByText('Offsite copy')).toBeTruthy();
    expect(screen.getByText('In sync')).toBeTruthy();
    expect(screen.getByText('30 minutes ago')).toBeTruthy();
    expect(screen.getByText('Nothing, up to date')).toBeTruthy();
    expect(screen.getByText('1.5 GB')).toBeTruthy();
    expect(screen.getByText('rclone:gdrive:peb')).toBeTruthy();
    expect(screen.queryByText(/may be out of date/)).toBeNull();
    expect(screen.queryByText(/has not been|have not been/)).toBeNull();
});

test('warns when the lag lasts more than 24 hours', async () => {
    renderCard(secondary({ lag_snapshots: 4, lag_since: '2026-10-23T10:00:00Z' }));
    await screen.findByText('Offsite copy');

    expect(screen.getByText('4 snapshot(s)')).toBeTruthy();
    expect(screen.getByText(/4 snapshot\(s\) have not been copied for more than 24 hours/)).toBeTruthy();
});

test('does not warn about a fresh lag', async () => {
    renderCard(secondary({ lag_snapshots: 1, lag_since: '2026-10-24T14:50:00Z', state: 'pending' }));
    await screen.findByText('Offsite copy');

    expect(screen.getByText('1 snapshot(s)')).toBeTruthy();
    expect(screen.queryByText(/for more than 24 hours/)).toBeNull();
});

test('warns and shows the error text when the last copy failed', async () => {
    renderCard(secondary({ state: 'error', last_copy_result: 'failed', last_error: 'copy: rclone: token expired' }));
    await screen.findByText('Offsite copy');

    expect(screen.getAllByText('Error').length).toBe(2);
    expect(screen.getByText('copy: rclone: token expired')).toBeTruthy();
    expect(screen.getByText(/The last copy failed/)).toBeTruthy();
});

test('shows a yellow line when the gateway warns that prune keeps being skipped, and when forgetting is stopped', async () => {
    const o = withSecondary(secondary({ forget_blocked: true, state: 'error', last_error: 'forget-secondary: too many' }));
    o.status.maintenance.prune_skipped_consecutive = 3;
    o.status.maintenance.warnings = ['prune was skipped as busy 3 times in a row'];
    render(<OffsiteCard overview={o} now={NOW} />, { wrapper: host!.Wrapper });
    await screen.findByText('Offsite copy');

    expect(screen.getByText(/Pruning the main repository was skipped 3 times in a row/)).toBeTruthy();
    expect(screen.getByText(/is stopped by a safety check/)).toBeTruthy();
    cleanup();
    render(<OffsiteCard overview={withSecondary(secondary())} now={NOW} />, { wrapper: host!.Wrapper });
    await screen.findByText('Offsite copy');
    expect(screen.queryByText(/Pruning the main repository/)).toBeNull();
    expect(pruneSkipWarning(withSecondary(secondary()))).toBe(0);
});

test('the copy now button starts a copy job and is disabled while a copy runs', async () => {
    mocked.runMaintenance.mockResolvedValue({ id: 77, job_ids: [77] });
    renderCard(secondary());
    fireEvent.click(await screen.findByRole('button', { name: 'Copy now' }));
    await waitFor(() => expect(mocked.runMaintenance).toHaveBeenCalledWith(['copy']));
    cleanup();
    renderCard(secondary({ state: 'copying', running: true }));
    const button = (await screen.findByRole('button', { name: 'Copy now' })) as HTMLButtonElement;
    expect(button.disabled).toBe(true);
    cleanup();
    render(<OffsiteCard overview={withSecondary(undefined)} now={NOW} />, { wrapper: host!.Wrapper });
    await screen.findByText('Offsite copy');
    expect(screen.queryByRole('button', { name: 'Copy now' })).toBeNull();
});

test('offsiteWarning covers failure, lag and the healthy case', () => {
    expect(offsiteWarning(secondary(), NOW)).toBeNull();
    expect(offsiteWarning(secondary({ last_copy_result: 'failed' }), NOW)?.failed).toBe(true);
    expect(offsiteWarning(secondary({ lag_snapshots: 2, lag_since: '2026-10-22T00:00:00Z' }), NOW)?.laggingFor24h).toBe(2);
    expect(offsiteWarning(secondary({ lag_snapshots: 2, lag_since: null }), NOW)).toBeNull();
    expect(offsiteWarning(secondary({ lag_snapshots: null }), NOW)).toBeNull();
});

test('dashboard shows the offsite card next to the storage target and the maintenance view names the new jobs', async () => {
    mocked.getOverview.mockResolvedValue(withSecondary(secondary()));
    mocked.getServers.mockResolvedValue(servers());
    mocked.getMaintenance.mockResolvedValue({
        items: [
            job({ id: 10, type: 'copy', message: 'copy finished', bytes_freed: null }),
            job({ id: 11, type: 'forget-secondary', message: '2 snapshots forgotten', bytes_freed: null }),
            job({ id: 12, type: 'prune-secondary', message: 'prune finished', bytes_freed: null }),
            job({ id: 13, type: 'check-secondary', message: 'no errors found', bytes_freed: null }),
        ],
    });
    render(<BackupsScreen />, { wrapper: host!.Wrapper });

    expect(await screen.findByText('Storage target')).toBeTruthy();
    expect(screen.getByText('Offsite copy')).toBeTruthy();
    fireEvent.click(screen.getByRole('tab', { name: 'Maintenance' }));
    expect(await screen.findByText('Offsite: remove deleted')).toBeTruthy();
    expect(screen.getByText('Offsite: prune')).toBeTruthy();
    expect(screen.getByText('Offsite: check')).toBeTruthy();
    expect(screen.getAllByText('Offsite copy').length).toBeGreaterThan(0);
});

test('en and de keep the same keys and never use the colon-word syntax', () => {
    expect(Object.keys(de).sort()).toEqual(Object.keys(en).sort());
    for (const [key, value] of [...Object.entries(en), ...Object.entries(de)]) {
        expect(value, key).not.toMatch(/(^|\s):[a-z_]+/i);
    }
});

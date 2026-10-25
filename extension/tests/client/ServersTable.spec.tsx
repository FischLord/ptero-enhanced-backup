import { afterEach, beforeEach, expect, test, vi } from 'vitest';
import { cleanup, render, screen, within } from '@testing-library/react';
import { createExtensionTestHost } from '@pterodactyl/sdk/testing';
import ServersTable from '../../src/client/components/ServersTable';
import { servers } from './fixtures';

let host: ReturnType<typeof createExtensionTestHost> | undefined;

beforeEach(() => {
    host = createExtensionTestHost({ extensionId: 'peb-backups', prefix: 'peb', path: '/panel/backups' });
});

afterEach(() => {
    cleanup();
    host?.dispose();
    host = undefined;
});

const renderTable = () => render(<ServersTable rows={servers().items} warnHours={26} onOpen={vi.fn()} />, { wrapper: host!.Wrapper });

test('shows the stored column with the measured value and an explanation', async () => {
    renderTable();

    expect(await screen.findByText('Stored')).toBeTruthy();
    const value = await screen.findByText('2.15 GB');
    expect(value.getAttribute('title')).toMatch(/compressed, deduplicated/);
    expect(value.getAttribute('title')).toMatch(/column sum can exceed the total/);
});

test('shows a dash while a server has no measurement yet', async () => {
    renderTable();

    const row = (await screen.findByText(/^Unknown server/)).closest('tr')!;
    expect(within(row).getAllByText('–').length).toBeGreaterThan(0);
    expect(within(row).queryByTitle(/column sum can exceed the total/)).toBeNull();
});

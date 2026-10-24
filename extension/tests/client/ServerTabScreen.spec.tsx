import { afterEach, expect, test, vi } from 'vitest';
import { cleanup, render, screen } from '@testing-library/react';
import { createExtensionTestHost } from '@pterodactyl/sdk/testing';
import * as api from '../../src/client/api';
import { SERVER_A, backup } from './fixtures';
import ServerTabScreen from '../../src/client/screens/ServerTabScreen';

vi.mock('../../src/client/api', async (importOriginal) => ({ ...(await importOriginal<typeof import('../../src/client/api')>()), getBackups: vi.fn() }));

let host: ReturnType<typeof createExtensionTestHost> | undefined;
afterEach(() => {
    cleanup();
    host?.dispose();
    host = undefined;
});

test('lists the backups of the server whose admin page it is shown on', async () => {
    vi.mocked(api.getBackups).mockResolvedValue({ items: [backup()], total: 1, limit: 25, offset: 0 });
    host = createExtensionTestHost({
        extensionId: 'peb-backups',
        prefix: 'peb',
        resource: { kind: 'admin.server', resource: { object: 'server', attributes: { id: 4, uuid: SERVER_A, name: 'Satisfactory' } } as never },
    });
    render(<ServerTabScreen />, { wrapper: host.Wrapper });

    expect(await screen.findByRole('button', { name: 'Browse files' })).toBeTruthy();
    expect(api.getBackups).toHaveBeenCalledWith(SERVER_A, 1);
});

test('explains itself outside a server page', async () => {
    host = createExtensionTestHost({ extensionId: 'peb-backups', prefix: 'peb' });
    render(<ServerTabScreen />, { wrapper: host.Wrapper });

    expect(await screen.findByText(/only available on a server/)).toBeTruthy();
});

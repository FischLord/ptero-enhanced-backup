import { afterEach, expect, test, vi } from 'vitest';
import { cleanup, render, screen, waitFor } from '@testing-library/react';
import { createExtensionTestHost } from '@pterodactyl/sdk/testing';
import * as api from '../../src/client/api';
import ProHint from '../../src/client/components/ProHint';

vi.mock('../../src/client/api', async (importOriginal) => ({ ...(await importOriginal<typeof import('../../src/client/api')>()), getEdition: vi.fn() }));

let host: ReturnType<typeof createExtensionTestHost> | undefined;
afterEach(() => {
    cleanup();
    host?.dispose();
    host = undefined;
});

const edition = (installed: boolean) => ({ pro: { installed, id: installed ? 'peb-backups-pro' : null, version: installed ? '1.0.0' : null, features: [] } });

test('shows the Pro hint while Pro is not installed', async () => {
    vi.mocked(api.getEdition).mockResolvedValue(edition(false));
    host = createExtensionTestHost({ extensionId: 'peb-backups', prefix: 'peb' });
    render(<ProHint text={'pro_retention_hint'} />, { wrapper: host.Wrapper });

    expect(await screen.findByText(/Retention policies/)).toBeTruthy();
});

test('hides the Pro hint when Pro is installed', async () => {
    vi.mocked(api.getEdition).mockResolvedValue(edition(true));
    host = createExtensionTestHost({ extensionId: 'peb-backups', prefix: 'peb' });
    render(<ProHint text={'pro_retention_hint'} />, { wrapper: host.Wrapper });

    await waitFor(() => expect(api.getEdition).toHaveBeenCalled());
    expect(screen.queryByText(/Retention policies/)).toBeNull();
});

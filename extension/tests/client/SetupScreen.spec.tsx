import { afterEach, beforeEach, expect, test, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { createExtensionTestHost } from '@pterodactyl/sdk/testing';
import * as api from '../../src/client/api';
import type { CreatedGatewayServer, SelfTestResult, SetupState } from '../../src/client/types';
import SetupScreen from '../../src/client/screens/SetupScreen';

vi.mock('../../src/client/api', async (importOriginal) => ({
    ...(await importOriginal<typeof import('../../src/client/api')>()),
    getSetup: vi.fn(),
    importEgg: vi.fn(),
    getSetupNodes: vi.fn(),
    getSetupAllocations: vi.fn(),
    searchSetupUsers: vi.fn(),
    createGatewayServer: vi.fn(),
    runSelfTest: vi.fn(),
}));

const mocked = vi.mocked(api);
let host: ReturnType<typeof createExtensionTestHost> | undefined;

const setup = (over: Partial<SetupState> = {}): SetupState => ({
    available: { egg_import: true, egg_update: true, server_create: true, s3_probe: true },
    ready: false,
    checks: [
        { id: 'driver', status: 'fail', hint: 'driver_not_s3' },
        { id: 'endpoint', status: 'ok', hint: null },
        { id: 'part_size', status: 'warn', hint: 'part_size_large', value: 5368709120 },
        { id: 'admin_token', status: 'ok', hint: null },
    ],
    egg: { name: 'PEB Gateway', exists: false, id: null, error: null },
    defaults: { name: 'PEB Gateway', bucket: 'pterodactyl', region: 'us-east-1', repository: '', memory: 1024, disk: 20480 },
    snippets: {
        env: 'APP_BACKUP_DRIVER=s3\nAWS_ENDPOINT=<gateway address>\n',
        wings: 'system:\n  backups:\n    compression_level: none\n    restore_host_allowlist:\n      - <gateway host or IP>\n',
    },
    ...over,
});

const created = (): CreatedGatewayServer => ({
    server: { id: 12, uuid: 'u', name: 'PEB Gateway' },
    gateway_url: 'http://10.0.0.5:9000',
    credentials: { access_key: 'AKEXAMPLE0000000001', secret_key: 'S'.repeat(40), admin_token: 'T'.repeat(48), restic_password: 'R'.repeat(48), bucket: 'pterodactyl', region: 'us-east-1' },
    snippets: { env: 'AWS_ENDPOINT=http://10.0.0.5:9000\nAWS_ACCESS_KEY_ID=AKEXAMPLE0000000001\n', wings: '      - 10.0.0.5\n' },
});

beforeEach(() => {
    host = createExtensionTestHost({ extensionId: 'peb-backups', prefix: 'peb', path: '/panel/setup' });
    mocked.getSetup.mockResolvedValue(setup());
    mocked.getSetupNodes.mockResolvedValue({ items: [{ id: 1, name: 'node1', fqdn: 'n1.example.com', free_allocations: 3 }] });
    mocked.getSetupAllocations.mockResolvedValue({ items: [] });
    mocked.searchSetupUsers.mockResolvedValue({ items: [{ id: 1, username: 'admin', email: 'a@example.com' }] });
});

afterEach(() => {
    cleanup();
    host?.dispose();
    host = undefined;
    vi.clearAllMocks();
});

const renderScreen = () => render(<SetupScreen />, { wrapper: host!.Wrapper });

test('pre-check shows status per check with a fix hint for problems only, and no secrets', async () => {
    renderScreen();

    expect(await screen.findByText('Backup driver is s3')).toBeTruthy();
    const driver = screen.getByText('Backup driver is s3').closest('li')!;
    expect(driver.getAttribute('data-status')).toBe('fail');
    expect(within(driver).getByText('Missing')).toBeTruthy();
    expect(within(driver).getByText(/Set APP_BACKUP_DRIVER=s3/)).toBeTruthy();

    const endpoint = screen.getByText('S3 endpoint is set').closest('li')!;
    expect(endpoint.getAttribute('data-status')).toBe('ok');
    expect(within(endpoint).queryByText(/Set AWS_ENDPOINT/)).toBeNull();

    const part = screen.getByText('Maximum part size').closest('li')!;
    expect(within(part).getByText('Warning')).toBeTruthy();
    expect(within(part).getByText('5.37 GB')).toBeTruthy();
    expect(screen.getByText(/Some settings are missing/)).toBeTruthy();
});

test('a ready panel says so', async () => {
    mocked.getSetup.mockResolvedValue(setup({ ready: true, checks: [{ id: 'driver', status: 'ok', hint: null }] }));
    renderScreen();

    expect(await screen.findByText(/The panel is ready to send backups/)).toBeTruthy();
});

test('imports the egg when it is missing', async () => {
    mocked.importEgg.mockResolvedValue({ egg_id: 7, action: 'import' });
    renderScreen();

    expect(await screen.findByText('The egg is not in the panel yet.')).toBeTruthy();
    expect(screen.getByText('Import the egg first.')).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: 'Import egg' }));
    await waitFor(() => expect(mocked.importEgg).toHaveBeenCalledWith('import'));
});

test('offers an update instead of a duplicate when the egg exists', async () => {
    mocked.getSetup.mockResolvedValue(setup({ egg: { name: 'PEB Gateway', exists: true, id: 7, error: null } }));
    mocked.importEgg.mockResolvedValue({ egg_id: 7, action: 'update' });
    renderScreen();

    expect(await screen.findByText(/The egg already exists \(ID 7\)/)).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Import egg' })).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: 'Update egg' }));
    await waitFor(() => expect(mocked.importEgg).toHaveBeenCalledWith('update'));
});

test('falls back to manual instructions when the panel version lacks the classes', async () => {
    mocked.getSetup.mockResolvedValue(setup({ available: { egg_import: false, egg_update: false, server_create: false, s3_probe: true } }));
    renderScreen();

    expect(await screen.findByText('Egg import is not available in this panel version')).toBeTruthy();
    expect(screen.getByText('Server creation is not available in this panel version')).toBeTruthy();
    expect(screen.getByText(/Admin > Eggs > Import/)).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Import egg' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Create gateway server' })).toBeNull();
});

test('the server form is shown once the egg exists and stays disabled until complete', async () => {
    mocked.getSetup.mockResolvedValue(setup({ egg: { name: 'PEB Gateway', exists: true, id: 7, error: null } }));
    renderScreen();

    const button = await screen.findByRole('button', { name: 'Create gateway server' });
    expect((button as HTMLButtonElement).disabled).toBe(true);
    expect((screen.getByLabelText('Server name') as HTMLInputElement).value).toBe('PEB Gateway');
    expect((screen.getByLabelText('Bucket') as HTMLInputElement).value).toBe('pterodactyl');
    expect(mocked.createGatewayServer).not.toHaveBeenCalled();
});

test('snippets show placeholders before a server exists', async () => {
    renderScreen();

    const env = await screen.findByLabelText('Panel .env');
    expect(env.textContent).toContain('AWS_ENDPOINT=<gateway address>');
    expect(screen.getByLabelText('Wings config.yml').textContent).toContain('compression_level: none');
    expect(screen.getByText(/Values in angle brackets are placeholders/)).toBeTruthy();
});

test('self-test shows ok, warning and failure with their hints', async () => {
    const result: SelfTestResult = {
        ok: false,
        checks: [
            { id: 'gateway', status: 'ok', hint: null },
            { id: 'token', status: 'fail', hint: 'token_rejected' },
            { id: 's3', status: 'warn', hint: 's3_abort_failed' },
            { id: 'https', status: 'warn', hint: 'not_https' },
        ],
    };
    mocked.runSelfTest.mockResolvedValue(result);
    renderScreen();

    fireEvent.click(await screen.findByRole('button', { name: 'Run self-test' }));

    expect(await screen.findByText('At least one check failed. See the hints.')).toBeTruthy();
    const token = screen.getByText('Admin token is valid').closest('li')!;
    expect(token.getAttribute('data-status')).toBe('fail');
    expect(within(token).getByText(/The gateway rejected the token/)).toBeTruthy();
    expect(screen.getByText('Addresses use HTTPS').closest('li')!.getAttribute('data-status')).toBe('warn');
    expect(screen.getByText('Gateway is reachable').closest('li')!.getAttribute('data-status')).toBe('ok');
});

test('self-test reports a request error instead of crashing', async () => {
    mocked.runSelfTest.mockRejectedValue({ response: { status: 500, data: { error: 'x', message: 'boom' } } });
    renderScreen();

    fireEvent.click(await screen.findByRole('button', { name: 'Run self-test' }));
    expect(await screen.findByText('The self-test could not be run.')).toBeTruthy();
});

test('shows the load error with a retry when the setup state cannot be fetched', async () => {
    mocked.getSetup.mockRejectedValueOnce({ response: { status: 500, data: { error: 'x', message: 'Server exploded' } } });
    renderScreen();

    expect(await screen.findByText('Server exploded')).toBeTruthy();
});

test('credentials are shown once and gone after confirming', async () => {
    const { default: CredentialsPanel } = await import('../../src/client/components/setup/CredentialsPanel');
    const onDismiss = vi.fn();
    render(<CredentialsPanel created={created()} onDismiss={onDismiss} />, { wrapper: host!.Wrapper });

    expect(await screen.findByText('Store these values now')).toBeTruthy();
    expect(screen.getByText(/restic password is the key to all backups/)).toBeTruthy();
    expect(screen.getByLabelText('Restic password').textContent).toBe('R'.repeat(48));
    expect(screen.getByLabelText('Admin token (enter it in the extension settings)').textContent).toBe('T'.repeat(48));
    fireEvent.click(screen.getByRole('button', { name: 'I have stored them' }));
    expect(onDismiss).toHaveBeenCalled();
});

test('shows the egg update note, the owner note and the gateway address field', async () => {
    mocked.getSetup.mockResolvedValue(setup({ egg: { name: 'PEB Gateway', exists: true, id: 7, error: null } }));
    mocked.getSetupAllocations.mockResolvedValue({ items: [{ id: 5, ip: '0.0.0.0', alias: '10.0.0.5', port: 9000 }] });
    mocked.createGatewayServer.mockRejectedValue(new Error('Network Error'));
    renderScreen();

    expect(await screen.findByText('Updating overwrites changes made to the egg in the panel.')).toBeTruthy();
    expect(screen.getByText(/Only root administrators can be the owner/)).toBeTruthy();
    expect(screen.getByLabelText('Gateway address')).toBeTruthy();
});

test('the secrets warning says they are also stored as egg variables', async () => {
    const { default: CredentialsPanel } = await import('../../src/client/components/setup/CredentialsPanel');
    render(<CredentialsPanel created={created()} onDismiss={() => {}} />, { wrapper: host!.Wrapper });

    expect(await screen.findByText(/saved as egg variables of the new server/)).toBeTruthy();
    expect(screen.queryByText(/not stored anywhere/)).toBeNull();
});

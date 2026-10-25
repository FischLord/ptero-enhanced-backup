import { afterEach, beforeEach, expect, test, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { createExtensionTestHost } from '@pterodactyl/sdk/testing';
import * as api from '../../src/client/api';
import NotificationsCard, { NOTIFY_DOCS_URL, isNotifyUnsupported } from '../../src/client/components/NotificationsCard';
import en from '../../src/client/lang/en.json';
import de from '../../src/client/lang/de.json';

vi.mock('../../src/client/api', async (importOriginal) => ({
    ...(await importOriginal<typeof import('../../src/client/api')>()),
    getNotifyConfig: vi.fn(),
    sendTestNotification: vi.fn(),
}));

const mocked = vi.mocked(api);
let host: ReturnType<typeof createExtensionTestHost> | undefined;

const httpError = (status: number, error?: string) => Object.assign(new Error('x'), { response: { status, data: error ? { error, message: 'm' } : {} } });

beforeEach(() => {
    host = createExtensionTestHost({ extensionId: 'peb-backups', prefix: 'peb', path: '/panel/backups' });
});

afterEach(() => {
    cleanup();
    host?.dispose();
    host = undefined;
    vi.clearAllMocks();
});

test('lists channels with redacted targets, format and language', async () => {
    mocked.getNotifyConfig.mockResolvedValue({
        lang: 'de',
        channels: [
            { type: 'webhook', target: 'https://discord.com', format: 'discord' },
            { type: 'smtp', target: 'smtp.example.com:587 -> 2 recipients' },
        ],
    });
    render(<NotificationsCard />, { wrapper: host!.Wrapper });

    expect(await screen.findByText('https://discord.com')).toBeTruthy();
    expect(screen.getByText('Format: discord')).toBeTruthy();
    expect(screen.getByText('smtp.example.com:587 -> 2 recipients')).toBeTruthy();
    expect(screen.getByText('de')).toBeTruthy();
});

test('without channels it explains the egg variables and links to the README', async () => {
    mocked.getNotifyConfig.mockResolvedValue({ lang: 'en', channels: [] });
    render(<NotificationsCard />, { wrapper: host!.Wrapper });

    expect(await screen.findByText(/PEB_NTFY_URL/)).toBeTruthy();
    expect(screen.getByRole('link', { name: en.notify_docs_link }).getAttribute('href')).toBe(NOTIFY_DOCS_URL);
    expect(screen.queryByRole('button', { name: 'Send test notification' })).toBeNull();
});

test('test button shows per-channel results and is disabled while running', async () => {
    mocked.getNotifyConfig.mockResolvedValue({ lang: 'en', channels: [{ type: 'ntfy', target: 'https://ntfy.sh' }] });
    let resolve!: (v: Awaited<ReturnType<typeof api.sendTestNotification>>) => void;
    mocked.sendTestNotification.mockReturnValue(new Promise((r) => (resolve = r)));
    render(<NotificationsCard />, { wrapper: host!.Wrapper });

    fireEvent.click(await screen.findByRole('button', { name: 'Send test notification' }));
    const running = (await screen.findByRole('button', { name: 'Sending...' })) as HTMLButtonElement;
    expect(running.disabled).toBe(true);

    resolve({ results: [{ type: 'ntfy', target: 'https://ntfy.sh', ok: true }, { type: 'webhook', target: 'https://discord.com', ok: false, error: 'HTTP 404' }] });
    expect(await screen.findByText('Delivered')).toBeTruthy();
    expect(screen.getByText('Failed')).toBeTruthy();
    expect(screen.getByText('HTTP 404')).toBeTruthy();
});

test('a 429 on the test shows no result list and re-enables the button', async () => {
    mocked.getNotifyConfig.mockResolvedValue({ lang: 'en', channels: [{ type: 'ntfy', target: 'https://ntfy.sh' }] });
    mocked.sendTestNotification.mockRejectedValue(httpError(429, 'rate_limited'));
    render(<NotificationsCard />, { wrapper: host!.Wrapper });

    fireEvent.click(await screen.findByRole('button', { name: 'Send test notification' }));
    await waitFor(() => expect(mocked.sendTestNotification).toHaveBeenCalled());
    await waitFor(() => expect((screen.getByRole('button', { name: 'Send test notification' }) as HTMLButtonElement).disabled).toBe(false));
    expect(screen.queryByText('Delivered')).toBeNull();
});

test('an older gateway gets the update hint instead of an error', async () => {
    mocked.getNotifyConfig.mockRejectedValue(httpError(404, 'not_supported'));
    render(<NotificationsCard />, { wrapper: host!.Wrapper });

    expect(await screen.findByText(/Update the gateway to 0\.3 or newer/)).toBeTruthy();
    expect(isNotifyUnsupported(httpError(404))).toBe(true);
    expect(isNotifyUnsupported(httpError(502, 'gateway_error'))).toBe(false);
});

test('German strings exist for every notification key', () => {
    for (const key of Object.keys(en).filter((k) => k.startsWith('notify_'))) expect(de[key as keyof typeof de]).toBeTruthy();
});

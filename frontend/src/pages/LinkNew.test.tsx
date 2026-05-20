// LinkNew flow coverage: provider gating, key minting, polling, expiry, cancel,
// and (critically) plaintext-key non-leakage.

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { axe } from 'jest-axe';

vi.mock('@/lib/platform', () => ({
  createMcpKey: vi.fn(),
  revokeMcpKey: vi.fn(),
  getViewer: vi.fn(),
}));
vi.mock('@/lib/bot-api', async () => {
  const actual = await vi.importActual<typeof import('@/lib/bot-api')>('@/lib/bot-api');
  return {
    ...actual,
    createLinkCode: vi.fn(),
    pollLinkCode: vi.fn(),
    getTelegramInfo: vi.fn(),
  };
});

import { createMcpKey, revokeMcpKey, getViewer } from '@/lib/platform';
import { createLinkCode, pollLinkCode, getTelegramInfo } from '@/lib/bot-api';
import LinkNew from './LinkNew';

const PLAINTEXT_KEY = 'mk_super_secret_xyz_should_never_leak';
const KEY_ID = '11111111-1111-1111-1111-111111111111';
const CODE = '123456';

function renderLinkNew(initial = '/link/new') {
  return render(
    <MemoryRouter initialEntries={[initial]}>
      <Routes>
        <Route path="/link/new" element={<LinkNew />} />
        <Route path="/links/:id" element={<div>landed at link {/* id */}</div>} />
      </Routes>
    </MemoryRouter>,
  );
}

// waitForPick resolves once the bot-status check completes and the provider
// picker is on screen — every mint/poll test starts there.
async function waitForPick(): Promise<void> {
  await waitFor(() =>
    expect(screen.getByRole('button', { name: /generate code/i })).toBeInTheDocument(),
  );
}

beforeEach(() => {
  vi.mocked(createMcpKey).mockReset();
  vi.mocked(revokeMcpKey).mockReset();
  // Default: revoke returns a real resolved promise so unmount cleanup is safe.
  vi.mocked(revokeMcpKey).mockResolvedValue(undefined);
  vi.mocked(createLinkCode).mockReset();
  vi.mocked(pollLinkCode).mockReset();
  vi.mocked(getTelegramInfo).mockReset();
  vi.mocked(getViewer).mockReset();
  // Default: a bot IS connected, so the flow lands on the provider picker.
  vi.mocked(getTelegramInfo).mockResolvedValue({
    configured: true,
    username: 'moses_test_bot',
  });
  vi.mocked(getViewer).mockResolvedValue({ isTenantAdmin: false });

  // crypto.randomUUID may not exist on jsdom.
  if (!('crypto' in globalThis) || !globalThis.crypto.randomUUID) {
    Object.defineProperty(globalThis, 'crypto', {
      value: {
        ...((globalThis as { crypto?: Crypto }).crypto ?? {}),
        randomUUID: () => '00000000-0000-0000-0000-000000000000',
      },
      configurable: true,
    });
  }
});

afterEach(() => {
  vi.useRealTimers();
});

describe('<LinkNew />', () => {
  it('shows an honest "no bot connected" message when configured=false (non-admin)', async () => {
    vi.mocked(getTelegramInfo).mockResolvedValueOnce({ configured: false });
    vi.mocked(getViewer).mockResolvedValueOnce({ isTenantAdmin: false });

    renderLinkNew();

    await waitFor(() =>
      expect(screen.getByText(/no telegram bot connected yet/i)).toBeInTheDocument(),
    );
    expect(
      screen.getByText(/your tenant admin has not connected a telegram bot yet/i),
    ).toBeInTheDocument();
    // A non-admin must NOT see the Connect wizard link.
    expect(screen.queryByRole('link', { name: /connect telegram/i })).toBeNull();
  });

  it('offers the Connect wizard link to a tenant admin when no bot is connected', async () => {
    vi.mocked(getTelegramInfo).mockResolvedValueOnce({ configured: false });
    vi.mocked(getViewer).mockResolvedValueOnce({ isTenantAdmin: true });

    renderLinkNew();

    const wizardLink = await screen.findByRole('link', { name: /connect telegram/i });
    expect(wizardLink).toHaveAttribute('href', '/settings/telegram');
  });

  it('never shows a fabricated @moses_<tenant>_bot handle', async () => {
    const { container } = renderLinkNew();
    await waitForPick();
    expect(container.innerHTML).not.toContain('@moses_');
    expect(container.innerHTML).not.toContain('<tenant>');
  });

  it('only enables Telegram in the provider picker', async () => {
    renderLinkNew();
    await waitForPick();
    const telegram = screen.getByRole('radio', { name: /telegram/i }) as HTMLInputElement;
    const discord = screen.getByRole('radio', { name: /discord/i }) as HTMLInputElement;
    expect(telegram.disabled).toBe(false);
    expect(telegram.checked).toBe(true);
    expect(discord.disabled).toBe(true);
  });

  it('shows the real bot @username in the claim instructions', async () => {
    vi.mocked(getTelegramInfo).mockResolvedValueOnce({
      configured: true,
      username: 'moses_acme_bot',
    });
    vi.mocked(createMcpKey).mockResolvedValueOnce({ keyId: KEY_ID, key: PLAINTEXT_KEY });
    vi.mocked(createLinkCode).mockResolvedValueOnce({
      code: CODE,
      expiresAt: new Date(Date.now() + 60_000).toISOString(),
    });
    vi.mocked(pollLinkCode).mockResolvedValue({ status: 'pending' });

    renderLinkNew();
    await waitForPick();
    fireEvent.click(screen.getByRole('button', { name: /generate code/i }));

    await waitFor(() =>
      expect(screen.getByText(/enter this code in telegram/i)).toBeInTheDocument(),
    );
    expect(screen.getByText('@moses_acme_bot')).toBeInTheDocument();
  });

  it('calls createMcpKey then createLinkCode in order on Generate', async () => {
    vi.mocked(createMcpKey).mockResolvedValueOnce({ keyId: KEY_ID, key: PLAINTEXT_KEY });
    vi.mocked(createLinkCode).mockResolvedValueOnce({
      code: CODE,
      expiresAt: new Date(Date.now() + 60_000).toISOString(),
    });
    vi.mocked(pollLinkCode).mockResolvedValue({ status: 'pending' });

    const { container } = renderLinkNew();
    await waitForPick();
    fireEvent.click(screen.getByRole('button', { name: /generate code/i }));

    await waitFor(() => expect(createMcpKey).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(createLinkCode).toHaveBeenCalledTimes(1));
    // Order: createLinkCode must run after createMcpKey resolves.
    const platformOrder = vi.mocked(createMcpKey).mock.invocationCallOrder[0];
    const botOrder = vi.mocked(createLinkCode).mock.invocationCallOrder[0];
    expect(botOrder).toBeGreaterThan(platformOrder);

    // createLinkCode received the plaintext key + hint.
    expect(vi.mocked(createLinkCode).mock.calls[0][0]).toMatchObject({
      apiKey: PLAINTEXT_KEY,
      apiKeyIdHint: KEY_ID,
      expiresInSeconds: 60,
    });

    // Code is displayed.
    await waitFor(() => expect(screen.getByText(/enter this code in telegram/i)).toBeInTheDocument());

    // Plaintext key must NOT appear in the DOM.
    expect(container.innerHTML).not.toContain(PLAINTEXT_KEY);
  });

  it('renders a countdown timer when the code is shown', async () => {
    // The actual tick logic is exercised in CountdownTimer.test.tsx; here we
    // just verify the timer is mounted with the right initial value once the
    // code-display step is reached.
    vi.mocked(createMcpKey).mockResolvedValueOnce({ keyId: KEY_ID, key: PLAINTEXT_KEY });
    vi.mocked(createLinkCode).mockResolvedValueOnce({
      code: CODE,
      expiresAt: new Date(Date.now() + 60_000).toISOString(),
    });
    vi.mocked(pollLinkCode).mockResolvedValue({ status: 'pending' });

    renderLinkNew();
    await waitForPick();
    fireEvent.click(screen.getByRole('button', { name: /generate code/i }));
    await waitFor(() => expect(screen.getByTestId('countdown-value')).toBeInTheDocument());

    expect(screen.getByTestId('countdown-value').textContent).toMatch(
      /^00:(59|60)$|^01:00$/,
    );
  });

  it('navigates to /links/:id when polling reports completed', async () => {
    vi.mocked(createMcpKey).mockResolvedValueOnce({ keyId: KEY_ID, key: PLAINTEXT_KEY });
    vi.mocked(createLinkCode).mockResolvedValueOnce({
      code: CODE,
      expiresAt: new Date(Date.now() + 60_000).toISOString(),
    });
    // Immediate completion on first poll.
    vi.mocked(pollLinkCode).mockResolvedValueOnce({
      status: 'completed',
      linkId: 'link-xyz',
    });

    renderLinkNew();
    await waitForPick();
    fireEvent.click(screen.getByRole('button', { name: /generate code/i }));

    await waitFor(() => expect(screen.getByText(/linked successfully/i)).toBeInTheDocument());

    // Auto-redirect after 2s. waitFor with a generous timeout drives the real
    // setTimeout via the test event loop.
    await waitFor(
      () => expect(screen.getByText(/landed at link/i)).toBeInTheDocument(),
      { timeout: 3000 },
    );
    // Successful link must NOT trigger a revoke.
    expect(revokeMcpKey).not.toHaveBeenCalled();
  });

  it('shows retry + revokes the key when the server reports 410 expired', async () => {
    vi.mocked(createMcpKey).mockResolvedValueOnce({ keyId: KEY_ID, key: PLAINTEXT_KEY });
    vi.mocked(createLinkCode).mockResolvedValueOnce({
      code: CODE,
      expiresAt: new Date(Date.now() + 60_000).toISOString(),
    });
    vi.mocked(pollLinkCode).mockRejectedValueOnce({
      status: 410,
      code: 'expired',
      message: 'gone',
    });
    vi.mocked(revokeMcpKey).mockResolvedValueOnce(undefined);

    renderLinkNew();
    await waitForPick();
    fireEvent.click(screen.getByRole('button', { name: /generate code/i }));

    await waitFor(() => expect(screen.getByText(/code expired/i)).toBeInTheDocument());
    expect(revokeMcpKey).toHaveBeenCalledWith(KEY_ID);
    expect(screen.getByRole('button', { name: /try again/i })).toBeInTheDocument();
  });

  // Accessibility — axe scans on both reachable states (pick + code).
  // These guarantee the page meets WCAG 2.1 AA the same way Dashboard does;
  // the original T-FE-2 review claimed axe coverage but the asserts were
  // missing — added here as part of the CHAT-y3u follow-up.
  it('has no axe violations in the provider-pick state', async () => {
    const { container } = renderLinkNew();
    await waitForPick();
    const results = await axe(container);
    expect(results).toHaveNoViolations();
  });

  it('has no axe violations in the not-connected state', async () => {
    vi.mocked(getTelegramInfo).mockResolvedValueOnce({ configured: false });
    vi.mocked(getViewer).mockResolvedValueOnce({ isTenantAdmin: true });
    const { container } = renderLinkNew();
    await waitFor(() =>
      expect(screen.getByText(/no telegram bot connected yet/i)).toBeInTheDocument(),
    );
    const results = await axe(container);
    expect(results).toHaveNoViolations();
  });

  it('has no axe violations once the code is displayed', async () => {
    vi.mocked(createMcpKey).mockResolvedValueOnce({ keyId: KEY_ID, key: PLAINTEXT_KEY });
    vi.mocked(createLinkCode).mockResolvedValueOnce({
      code: CODE,
      expiresAt: new Date(Date.now() + 60_000).toISOString(),
    });
    vi.mocked(pollLinkCode).mockResolvedValue({ status: 'pending' });

    const { container } = renderLinkNew();
    await waitForPick();
    fireEvent.click(screen.getByRole('button', { name: /generate code/i }));
    await waitFor(() =>
      expect(screen.getByText(/enter this code in telegram/i)).toBeInTheDocument(),
    );

    const results = await axe(container);
    expect(results).toHaveNoViolations();
  });

  it('cancel button revokes the key and returns to step 1', async () => {
    vi.mocked(createMcpKey).mockResolvedValueOnce({ keyId: KEY_ID, key: PLAINTEXT_KEY });
    vi.mocked(createLinkCode).mockResolvedValueOnce({
      code: CODE,
      expiresAt: new Date(Date.now() + 60_000).toISOString(),
    });
    vi.mocked(pollLinkCode).mockResolvedValue({ status: 'pending' });
    vi.mocked(revokeMcpKey).mockResolvedValueOnce(undefined);

    renderLinkNew();
    await waitForPick();
    fireEvent.click(screen.getByRole('button', { name: /generate code/i }));
    await waitFor(() => expect(screen.getByText(/enter this code in telegram/i)).toBeInTheDocument());

    fireEvent.click(screen.getByRole('button', { name: /cancel/i }));
    await waitFor(() => expect(revokeMcpKey).toHaveBeenCalledWith(KEY_ID));
    await waitFor(() =>
      expect(screen.getByRole('button', { name: /generate code/i })).toBeInTheDocument(),
    );
  });
});

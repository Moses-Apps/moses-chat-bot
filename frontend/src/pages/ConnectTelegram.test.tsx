// ConnectTelegram wizard coverage: admin gating, BotFather hand-off, the
// token-paste happy path, disconnect, and accessibility.

import { describe, it, expect, beforeEach, vi } from 'vitest';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { axe } from 'jest-axe';

vi.mock('@/lib/platform', () => ({
  getViewer: vi.fn(),
}));
vi.mock('@/lib/bot-api', async () => {
  const actual = await vi.importActual<typeof import('@/lib/bot-api')>('@/lib/bot-api');
  return {
    ...actual,
    getTelegramInfo: vi.fn(),
    connectTelegram: vi.fn(),
    disconnectTelegram: vi.fn(),
  };
});

import { getViewer } from '@/lib/platform';
import {
  getTelegramInfo,
  connectTelegram,
  disconnectTelegram,
} from '@/lib/bot-api';
import ConnectTelegram from './ConnectTelegram';

function renderWizard() {
  return render(
    <MemoryRouter>
      <ConnectTelegram />
    </MemoryRouter>,
  );
}

beforeEach(() => {
  vi.mocked(getViewer).mockReset();
  vi.mocked(getTelegramInfo).mockReset();
  vi.mocked(connectTelegram).mockReset();
  vi.mocked(disconnectTelegram).mockReset();
});

describe('<ConnectTelegram />', () => {
  it('blocks non-admin viewers with a permission-required card', async () => {
    vi.mocked(getViewer).mockResolvedValueOnce({ isTenantAdmin: false });

    renderWizard();

    await waitFor(() =>
      expect(screen.getByText(/tenant admin required/i)).toBeInTheDocument(),
    );
    // The wizard's token field must not render for a non-admin.
    expect(screen.queryByLabelText(/bot token/i)).toBeNull();
    expect(getTelegramInfo).not.toHaveBeenCalled();
  });

  it('renders the BotFather hand-off and token field for an admin', async () => {
    vi.mocked(getViewer).mockResolvedValueOnce({ isTenantAdmin: true });
    vi.mocked(getTelegramInfo).mockResolvedValueOnce({ configured: false });

    renderWizard();

    // BotFather deep link present — a real https://t.me/botfather link.
    const link = await screen.findByRole('link', { name: /open @botfather/i });
    expect(link).toHaveAttribute('href', 'https://t.me/botfather');
    // The /newbot command is shown for copy-paste.
    expect(screen.getByText('/newbot')).toBeInTheDocument();
    // Token paste field is present.
    expect(screen.getByLabelText(/bot token/i)).toBeInTheDocument();
  });

  it('connects the bot on the happy path and shows the @username', async () => {
    vi.mocked(getViewer).mockResolvedValueOnce({ isTenantAdmin: true });
    vi.mocked(getTelegramInfo).mockResolvedValueOnce({ configured: false });
    vi.mocked(connectTelegram).mockResolvedValueOnce({
      configured: true,
      username: 'moses_acme_bot',
    });

    renderWizard();

    const input = await screen.findByLabelText(/bot token/i);
    fireEvent.change(input, { target: { value: '123456789:ABCtoken' } });
    fireEvent.click(screen.getByRole('button', { name: /connect bot/i }));

    await waitFor(() =>
      expect(screen.getByText(/telegram bot connected/i)).toBeInTheDocument(),
    );
    expect(screen.getByText('@moses_acme_bot')).toBeInTheDocument();
    expect(connectTelegram).toHaveBeenCalledWith('123456789:ABCtoken');
  });

  it('surfaces a connect error without leaving the wizard', async () => {
    vi.mocked(getViewer).mockResolvedValueOnce({ isTenantAdmin: true });
    vi.mocked(getTelegramInfo).mockResolvedValueOnce({ configured: false });
    vi.mocked(connectTelegram).mockRejectedValueOnce({
      status: 400,
      code: 'bad_request',
      message: 'Telegram rejected this token.',
    });

    renderWizard();

    const input = await screen.findByLabelText(/bot token/i);
    fireEvent.change(input, { target: { value: 'bogus' } });
    fireEvent.click(screen.getByRole('button', { name: /connect bot/i }));

    await waitFor(() =>
      expect(screen.getByRole('alert')).toHaveTextContent(/telegram rejected/i),
    );
    // Still on the wizard.
    expect(screen.getByLabelText(/bot token/i)).toBeInTheDocument();
  });

  it('shows the connected state and supports disconnect', async () => {
    vi.mocked(getViewer).mockResolvedValueOnce({ isTenantAdmin: true });
    vi.mocked(getTelegramInfo).mockResolvedValueOnce({
      configured: true,
      username: 'moses_existing_bot',
    });
    vi.mocked(disconnectTelegram).mockResolvedValueOnce(undefined);

    renderWizard();

    await waitFor(() =>
      expect(screen.getByText(/telegram bot connected/i)).toBeInTheDocument(),
    );
    expect(screen.getByText('@moses_existing_bot')).toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: /disconnect bot/i }));
    await waitFor(() => expect(disconnectTelegram).toHaveBeenCalledTimes(1));
    await waitFor(() =>
      expect(screen.getByText(/create a bot with botfather/i)).toBeInTheDocument(),
    );
  });

  it('has no axe violations in the wizard state', async () => {
    vi.mocked(getViewer).mockResolvedValueOnce({ isTenantAdmin: true });
    vi.mocked(getTelegramInfo).mockResolvedValueOnce({ configured: false });

    const { container } = renderWizard();
    await screen.findByLabelText(/bot token/i);
    const results = await axe(container);
    expect(results).toHaveNoViolations();
  });

  it('has no axe violations in the forbidden state', async () => {
    vi.mocked(getViewer).mockResolvedValueOnce({ isTenantAdmin: false });

    const { container } = renderWizard();
    await waitFor(() =>
      expect(screen.getByText(/tenant admin required/i)).toBeInTheDocument(),
    );
    const results = await axe(container);
    expect(results).toHaveNoViolations();
  });
});

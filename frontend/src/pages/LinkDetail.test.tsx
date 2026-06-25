// LinkDetail covers tab switching + the unlink dialog flow.

import { describe, it, expect, beforeEach, vi } from 'vitest';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { axe } from 'jest-axe';
import type { Link, Message } from '@/lib/bot-api';

vi.mock('@/lib/bot-api', async () => {
  const actual = await vi.importActual<typeof import('@/lib/bot-api')>('@/lib/bot-api');
  return {
    ...actual,
    listLinks: vi.fn(),
    getLinkMessages: vi.fn(),
    deleteLink: vi.fn(),
  };
});

import { listLinks, getLinkMessages, deleteLink } from '@/lib/bot-api';
import { withQueryClient } from '@/test/queryWrapper';
import LinkDetail from './LinkDetail';

const link: Link = {
  id: 'link-1',
  provider: 'telegram',
  providerUserId: '12345',
  isActive: true,
  createdAt: '2026-05-19T00:00:00Z',
  lastUsedAt: '2026-05-19T10:00:00Z',
};

const messages: Message[] = [
  {
    id: 'm-1',
    linkId: 'link-1',
    direction: 'in',
    text: 'hello bot',
    occurredAt: '2026-05-19T10:00:00Z',
  },
  {
    id: 'm-2',
    linkId: 'link-1',
    direction: 'out',
    text: 'hi human',
    occurredAt: '2026-05-19T10:00:05Z',
  },
];

function renderDetail() {
  return render(
    withQueryClient(
      <MemoryRouter initialEntries={['/links/link-1']}>
        <Routes>
          <Route path="/links/:id" element={<LinkDetail />} />
          <Route path="/" element={<div>back at home</div>} />
        </Routes>
      </MemoryRouter>,
    ),
  );
}

beforeEach(() => {
  vi.mocked(listLinks).mockResolvedValue([link]);
  vi.mocked(getLinkMessages).mockResolvedValue(messages);
  vi.mocked(deleteLink).mockReset();
  // Force the "sm+" tablist path so the visible tab buttons are findable;
  // jsdom defaults to a single window width.
  // (Both the select and the tablist render, but we drive the tablist.)
});

describe('<LinkDetail />', () => {
  it('renders the activity tab by default and lists messages', async () => {
    renderDetail();
    await waitFor(() => expect(screen.getByText('hello bot')).toBeInTheDocument());
    expect(screen.getByText('hi human')).toBeInTheDocument();
  });

  it('switches to settings and danger tabs', async () => {
    renderDetail();
    fireEvent.click(screen.getByRole('tab', { name: /settings/i }));
    await waitFor(() =>
      expect(screen.getByText(/settings ui lands in t-fe-3/i)).toBeInTheDocument(),
    );

    fireEvent.click(screen.getByRole('tab', { name: /danger/i }));
    await waitFor(() =>
      expect(screen.getByRole('button', { name: /^unlink$/i })).toBeInTheDocument(),
    );
  });

  // Accessibility — axe scan on the default (Activity) tab. The original
  // T-FE-2 review claimed axe coverage for this page but the assert was
  // missing; added here as part of the CHAT-y3u follow-up.
  it('has no axe violations on the activity tab', async () => {
    const { container } = renderDetail();
    // Wait for the messages to render so the scan covers populated DOM.
    await waitFor(() => expect(screen.getByText('hello bot')).toBeInTheDocument());
    const results = await axe(container);
    expect(results).toHaveNoViolations();
  });

  it('Unlink button opens a confirm dialog, confirm calls unlink + redirects', async () => {
    vi.mocked(deleteLink).mockResolvedValueOnce(undefined);
    renderDetail();
    fireEvent.click(screen.getByRole('tab', { name: /danger/i }));
    fireEvent.click(screen.getByRole('button', { name: /^unlink$/i }));

    // Dialog appears.
    const dialog = await screen.findByRole('dialog');
    expect(dialog).toHaveAttribute('aria-modal', 'true');
    expect(dialog).toHaveTextContent(/unlink this chat\?/i);

    fireEvent.click(
      screen.getAllByRole('button', { name: /^unlink$/i }).find((el) =>
        el.hasAttribute('data-dialog-confirm'),
      )!,
    );

    await waitFor(() => expect(deleteLink).toHaveBeenCalledWith('link-1'));
    await waitFor(() => expect(screen.getByText(/back at home/i)).toBeInTheDocument());
  });
});

// Dashboard renders + axe + state coverage.

import { describe, it, expect, beforeEach, vi } from 'vitest';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { axe } from 'jest-axe';
import type { Link } from '@/lib/bot-api';

vi.mock('@/lib/bot-api', async () => {
  const actual = await vi.importActual<typeof import('@/lib/bot-api')>('@/lib/bot-api');
  return {
    ...actual,
    listLinks: vi.fn(),
    deleteLink: vi.fn(),
  };
});

import { listLinks } from '@/lib/bot-api';
import { withQueryClient } from '@/test/queryWrapper';
import Dashboard from './Dashboard';

const sampleLinks: Link[] = [
  {
    id: 'link-1',
    provider: 'telegram',
    providerUserId: '12345',
    isActive: true,
    createdAt: '2026-05-19T00:00:00Z',
    lastUsedAt: '2026-05-19T10:00:00Z',
  },
];

function renderDashboard() {
  return render(
    withQueryClient(
      <MemoryRouter>
        <Dashboard />
      </MemoryRouter>,
    ),
  );
}

describe('<Dashboard />', () => {
  beforeEach(() => {
    vi.mocked(listLinks).mockReset();
  });

  it('renders the empty state with a CTA when no links exist', async () => {
    vi.mocked(listLinks).mockResolvedValueOnce([]);
    const { container } = renderDashboard();
    await waitFor(() => expect(listLinks).toHaveBeenCalledTimes(1));
    await waitFor(() =>
      expect(screen.getByText(/connect your first chat/i)).toBeInTheDocument(),
    );
    // There are two CTAs — one in the empty card, one in the "Link a new chat" card.
    const ctas = screen.getAllByRole('link', { name: /link/i }).filter(
      (el) => el.getAttribute('href') === '/link/new',
    );
    expect(ctas.length).toBeGreaterThanOrEqual(1);
    const results = await axe(container);
    expect(results).toHaveNoViolations();
  });

  it('renders link cards when the store has links', async () => {
    vi.mocked(listLinks).mockResolvedValueOnce(sampleLinks);
    const { container } = renderDashboard();
    await waitFor(() => expect(screen.getByText('Telegram')).toBeInTheDocument());
    expect(screen.getByText('12345')).toBeInTheDocument();
    const results = await axe(container);
    expect(results).toHaveNoViolations();
  });

  it('shows an error banner with a working retry button on fetch failure', async () => {
    vi.mocked(listLinks).mockRejectedValueOnce({
      status: 500,
      code: 'internal_error',
      message: 'boom',
    });
    renderDashboard();
    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('boom'));

    vi.mocked(listLinks).mockResolvedValueOnce(sampleLinks);
    fireEvent.click(screen.getByRole('button', { name: /retry/i }));
    await waitFor(() => expect(screen.getByText('12345')).toBeInTheDocument());
  });
});

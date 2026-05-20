// Messages page — filter wiring, infinite scroll, axe clean.

import { describe, it, expect, beforeEach, vi } from 'vitest';
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { axe } from 'jest-axe';
import type { Link, Message, SearchMessagesResponse } from '@/lib/bot-api';

vi.mock('@/lib/bot-api', async () => {
  const actual = await vi.importActual<typeof import('@/lib/bot-api')>('@/lib/bot-api');
  return {
    ...actual,
    listLinks: vi.fn(),
    searchMessages: vi.fn(),
  };
});

import { listLinks, searchMessages } from '@/lib/bot-api';
import { useLinksStore } from '@/stores/linksStore';
import { useMessagesStore, EMPTY_FILTERS } from '@/stores/messagesStore';
import Messages from './Messages';

const link: Link = {
  id: 'l-1',
  provider: 'telegram',
  providerUserId: '12345',
  providerDisplayName: 'Alice',
  isActive: true,
  createdAt: '2026-05-19T00:00:00Z',
};

const link2: Link = {
  id: 'l-2',
  provider: 'telegram',
  providerUserId: '67890',
  providerDisplayName: 'Bob',
  isActive: true,
  createdAt: '2026-05-19T00:00:00Z',
};

const m1: Message = {
  id: 'm-1',
  linkId: 'l-1',
  direction: 'in',
  text: 'hello world',
  occurredAt: '2026-05-19T10:00:00Z',
};
const m2: Message = {
  id: 'm-2',
  linkId: 'l-1',
  direction: 'out',
  text: 'goodbye world',
  occurredAt: '2026-05-19T10:00:30Z',
};
const m3: Message = {
  id: 'm-3',
  linkId: 'l-2',
  direction: 'in',
  text: 'second link hi',
  occurredAt: '2026-05-19T10:01:00Z',
};

function resetStores(): void {
  useLinksStore.setState({
    links: [link, link2],
    currentLink: null,
    loading: false,
    error: null,
  });
  useMessagesStore.setState({
    pageMessages: [],
    filters: { ...EMPTY_FILTERS },
    cursor: null,
    hasMore: false,
    searching: false,
    searchError: null,
  });
}

function renderPage() {
  return render(
    <MemoryRouter>
      <Messages />
    </MemoryRouter>,
  );
}

describe('<Messages />', () => {
  beforeEach(() => {
    resetStores();
    vi.mocked(listLinks).mockResolvedValue([link, link2]);
    vi.mocked(searchMessages).mockReset();
  });

  it('renders rows fetched from the API', async () => {
    vi.mocked(searchMessages).mockResolvedValueOnce({
      messages: [m1, m2, m3],
    } as SearchMessagesResponse);
    renderPage();
    await waitFor(() => expect(screen.getByText('hello world')).toBeInTheDocument());
    expect(screen.getByText('goodbye world')).toBeInTheDocument();
    expect(screen.getByText('second link hi')).toBeInTheDocument();
  });

  it('client-filters by direction', async () => {
    vi.mocked(searchMessages).mockResolvedValue({
      messages: [m1, m2, m3],
    } as SearchMessagesResponse);
    renderPage();
    await waitFor(() => expect(screen.getByText('hello world')).toBeInTheDocument());
    // Direction control renders in both the sm+ row and the <sm accordion;
    // both are mounted in jsdom. Click the first match.
    const outboundButtons = screen.getAllByRole('button', { name: /^outbound$/i });
    fireEvent.click(outboundButtons[0]);
    await waitFor(() => {
      expect(screen.queryByText('hello world')).toBeNull();
      expect(screen.getByText('goodbye world')).toBeInTheDocument();
    });
  });

  it('client-filters by free-text after the debounce settles', async () => {
    vi.useFakeTimers();
    try {
      vi.mocked(searchMessages).mockResolvedValue({
        messages: [m1, m2, m3],
      } as SearchMessagesResponse);
      renderPage();
      // First fetch runs immediately under fake timers — flush pending promises.
      await act(async () => {
        await Promise.resolve();
      });
      await act(async () => {
        await Promise.resolve();
      });
      const search = screen.getByRole('searchbox', { name: /search message text/i });
      fireEvent.change(search, { target: { value: 'goodbye' } });
      // Debounce is 250ms. Before the window settles, nothing changes.
      act(() => {
        vi.advanceTimersByTime(249);
      });
      // After settle, the effect refetches + reapplies client filter.
      act(() => {
        vi.advanceTimersByTime(2);
      });
      // Flush microtasks for the await inside searchAll.
      await act(async () => {
        await Promise.resolve();
      });
      await act(async () => {
        await Promise.resolve();
      });
      expect(screen.queryByText('hello world')).toBeNull();
      expect(screen.getByText('goodbye world')).toBeInTheDocument();
    } finally {
      vi.useRealTimers();
    }
  });

  it('Load more button drives the next page', async () => {
    // First page: hasMore=true via nextCursor.
    vi.mocked(searchMessages)
      .mockResolvedValueOnce({ messages: [m1, m2], nextCursor: 'next-1' })
      .mockResolvedValueOnce({ messages: [m3] });
    renderPage();
    await waitFor(() => expect(screen.getByText('hello world')).toBeInTheDocument());
    // hasMore is true after first fetch.
    const more = await screen.findByRole('button', { name: /load more/i });
    fireEvent.click(more);
    await waitFor(() => expect(screen.getByText('second link hi')).toBeInTheDocument());
    expect(searchMessages).toHaveBeenCalledTimes(2);
  });

  it('shows the empty state when nothing matches', async () => {
    vi.mocked(searchMessages).mockResolvedValueOnce({ messages: [] });
    renderPage();
    await waitFor(() =>
      expect(screen.getByText(/no messages match your filters/i)).toBeInTheDocument(),
    );
  });

  it('shows the error banner with a working retry', async () => {
    vi.mocked(searchMessages).mockRejectedValueOnce({
      status: 500,
      code: 'internal_error',
      message: 'boom',
    });
    renderPage();
    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('boom'));
    vi.mocked(searchMessages).mockResolvedValueOnce({ messages: [m1] });
    fireEvent.click(screen.getByRole('button', { name: /retry/i }));
    await waitFor(() => expect(screen.getByText('hello world')).toBeInTheDocument());
  });

  it('has no axe violations once data is loaded', async () => {
    vi.mocked(searchMessages).mockResolvedValueOnce({ messages: [m1, m2] });
    const { container } = renderPage();
    await waitFor(() => expect(screen.getByText('hello world')).toBeInTheDocument());
    const results = await axe(container);
    expect(results).toHaveNoViolations();
  });
});

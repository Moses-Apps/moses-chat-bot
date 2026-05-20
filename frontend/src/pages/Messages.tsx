// Global Messages page — full-text search across every link the user owns.
//
// Layered architecture:
//   - `MessageFilters` owns the controlled inputs (text / chips / direction /
//     date), debouncing the text input itself.
//   - `useMessagesStore` owns the search state and the API-fetched + client-
//     filtered buffer. Each filter change triggers `searchAll()` to reset the
//     cursor and refetch.
//   - `MessageList` renders the buffer + an IntersectionObserver sentinel that
//     calls `loadMore()`.
//
// TODO(server-side filters): direction, full-text, and date range are still
// applied client-side after fetch (see bot-api.ts `searchMessages`). When the
// backend grows server-side support, swap the `toApiParams` helper in
// messagesStore.ts to forward the filters directly. Tracked in beads.

import { useEffect, type ReactElement } from 'react';
import BentoCard from '@/components/layout/BentoCard';
import MessageFilters from '@/components/messages/MessageFilters';
import MessageList from '@/components/messages/MessageList';
import { useLinksStore } from '@/stores/linksStore';
import { useMessagesStore } from '@/stores/messagesStore';

function SkeletonRow(): ReactElement {
  return (
    <li
      aria-hidden="true"
      className="flex min-h-[64px] items-center gap-4 rounded-bento border border-moses-border bg-moses-surface-raised p-4 dark:border-moses-border-dark dark:bg-moses-surface-dark-raised"
    >
      <span className="h-6 w-6 shrink-0 animate-pulse rounded-full bg-moses-surface-sunken dark:bg-moses-surface-dark-sunken" />
      <span className="flex-1 space-y-2">
        <span className="block h-3 w-24 animate-pulse rounded bg-moses-surface-sunken dark:bg-moses-surface-dark-sunken" />
        <span className="block h-3 w-72 animate-pulse rounded bg-moses-surface-sunken dark:bg-moses-surface-dark-sunken" />
      </span>
    </li>
  );
}

function EmptyState(): ReactElement {
  return (
    <div className="flex flex-col items-center gap-3 rounded-bento border border-dashed border-moses-border p-8 text-center dark:border-moses-border-dark">
      <svg viewBox="0 0 48 48" className="h-12 w-12 text-moses-accent" aria-hidden="true">
        <path
          fill="currentColor"
          d="M21 4a17 17 0 1 0 10.6 30.3l9 9 2.8-2.8-9-9A17 17 0 0 0 21 4m0 4a13 13 0 1 1 0 26 13 13 0 0 1 0-26"
        />
      </svg>
      <h3 className="text-base font-semibold">No messages match your filters</h3>
      <p className="max-w-sm text-sm text-moses-text-muted">
        Try widening the date range, clearing search text, or selecting different
        link chips.
      </p>
    </div>
  );
}

export default function Messages(): ReactElement {
  const links = useLinksStore((s) => s.links);
  const fetchLinks = useLinksStore((s) => s.fetchLinks);

  const filters = useMessagesStore((s) => s.filters);
  const pageMessages = useMessagesStore((s) => s.pageMessages);
  const hasMore = useMessagesStore((s) => s.hasMore);
  const searching = useMessagesStore((s) => s.searching);
  const searchError = useMessagesStore((s) => s.searchError);
  const setFilters = useMessagesStore((s) => s.setFilters);
  const searchAll = useMessagesStore((s) => s.searchAll);
  const loadMore = useMessagesStore((s) => s.loadMore);

  // Ensure we have links to populate the chip filter and the per-row label
  // resolution (provider icon + display name).
  useEffect(() => {
    if (links.length === 0) void fetchLinks();
  }, [links.length, fetchLinks]);

  // Re-run search whenever the filter shape changes. The store handles
  // resetting the cursor + buffer. Stringify the linkIds array first so the
  // dependency array stays statically checkable.
  const linkIdsKey = filters.linkIds.join(',');
  useEffect(() => {
    void searchAll();
    // searchAll is a stable store action; safe to omit per Zustand conventions.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [filters.q, filters.direction, filters.dateFrom, filters.dateTo, linkIdsKey]);

  const initialLoading = searching && pageMessages.length === 0;

  return (
    <div className="space-y-4">
      <MessageFilters links={links} filters={filters} onChange={setFilters} />

      <BentoCard title="Messages" subtitle="Relay history across every linked chat">
        {searchError ? (
          <div
            role="alert"
            className="flex flex-col gap-3 rounded-bento border border-moses-status-error/40 bg-moses-status-error/10 p-4 sm:flex-row sm:items-center sm:justify-between"
          >
            <p className="text-sm text-moses-status-error">
              Could not load messages: {searchError.message}
            </p>
            <button
              type="button"
              onClick={() => void searchAll()}
              className="min-h-[44px] rounded-bento border border-moses-status-error/50 bg-moses-surface-raised px-4 text-sm font-medium text-moses-status-error hover:bg-moses-status-error/10 focus:outline-none focus:ring-2 focus:ring-moses-status-error/40 dark:bg-moses-surface-dark-raised"
            >
              Retry
            </button>
          </div>
        ) : initialLoading ? (
          <ul className="space-y-2" aria-busy="true" aria-live="polite">
            <SkeletonRow />
            <SkeletonRow />
            <SkeletonRow />
            <SkeletonRow />
          </ul>
        ) : pageMessages.length === 0 ? (
          <EmptyState />
        ) : (
          <MessageList
            messages={pageMessages}
            links={links}
            hasMore={hasMore}
            loading={searching}
            onLoadMore={() => void loadMore()}
          />
        )}
      </BentoCard>
    </div>
  );
}

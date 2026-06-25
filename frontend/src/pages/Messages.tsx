// Global Messages page — full-text search across every link the user owns.
//
// Layered architecture:
//   - `MessageFilters` owns the controlled inputs (text / chips / direction /
//     date), debouncing the text input itself. The filter object is *client/UI
//     state* (component useState).
//   - `useSearchMessages` (TanStack useInfiniteQuery) owns the *server state*:
//     the cursor-paginated message buffer. Changing a filter changes the query
//     key → fresh first page; `fetchNextPage` advances pages within a key.
//   - `applyClientFilters` narrows the flattened pages by direction/text/date
//     (filters the backend doesn't accept yet).
//   - `MessageList` renders the buffer + an IntersectionObserver sentinel that
//     calls `fetchNextPage`.
//
// TODO(server-side filters): direction, full-text, and date range are still
// applied client-side after fetch (see bot-api.ts `searchMessages`). When the
// backend grows server-side support, forward them in `toApiParams`
// (api/messageFilters.ts) directly. Tracked in beads.

import { useMemo, useState, type ReactElement } from 'react';
import BentoCard from '@/components/layout/BentoCard';
import MessageFilters from '@/components/messages/MessageFilters';
import MessageList from '@/components/messages/MessageList';
import { useLinks, useSearchMessages } from '@/api/hooks';
import {
  EMPTY_FILTERS,
  applyClientFilters,
  type MessagesFilters,
} from '@/api/messageFilters';
import { getErrorMessage } from '@/lib/errors';

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
  // Links populate the chip filter and per-row label resolution (provider icon
  // + display name). Server state via TanStack Query.
  const { data: links = [] } = useLinks();

  // Filters are client/UI state.
  const [filters, setFilters] = useState<MessagesFilters>({ ...EMPTY_FILTERS });
  const patchFilters = (patch: Partial<MessagesFilters>) =>
    setFilters((prev) => ({ ...prev, ...patch }));

  // Server state: cursor-paginated buffer. The filter object is part of the
  // query key inside the hook, so changing a filter refetches the first page.
  const search = useSearchMessages(filters);

  // Flatten pages, then apply the client-side filters the backend can't.
  const pageMessages = useMemo(() => {
    const rows = search.data?.pages.flatMap((p) => p.messages) ?? [];
    return applyClientFilters(rows, filters);
  }, [search.data, filters]);

  const hasMore = Boolean(search.hasNextPage);
  // Any fetch (first page or next page) counts as "searching" for the spinner.
  const searching = search.isPending || search.isFetchingNextPage;
  const initialLoading = search.isPending;

  return (
    <div className="space-y-4">
      <MessageFilters links={links} filters={filters} onChange={patchFilters} />

      <BentoCard title="Messages" subtitle="Relay history across every linked chat">
        {search.isError ? (
          <div
            role="alert"
            className="flex flex-col gap-3 rounded-bento border border-moses-status-error/40 bg-moses-status-error/10 p-4 sm:flex-row sm:items-center sm:justify-between"
          >
            <p className="text-sm text-moses-status-error">
              Could not load messages: {getErrorMessage(search.error)}
            </p>
            <button
              type="button"
              onClick={() => void search.refetch()}
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
            onLoadMore={() => void search.fetchNextPage()}
          />
        )}
      </BentoCard>
    </div>
  );
}

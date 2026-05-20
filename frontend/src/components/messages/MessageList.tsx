// Infinite-scroll list for the global Messages page.
//
// - Renders MessageRow per item.
// - Trails an IntersectionObserver sentinel that fires `onLoadMore` whenever
//   it enters the viewport.
// - No virtualization for v1 — 50 rows × 10 pages = 500 DOM nodes is well
//   within budget on mobile.

import { useEffect, useRef, type ReactElement } from 'react';
import type { Link, Message } from '@/lib/bot-api';
import MessageRow from './MessageRow';

interface MessageListProps {
  messages: Message[];
  links: Link[];
  hasMore: boolean;
  loading: boolean;
  onLoadMore: () => void;
}

export function MessageList({
  messages,
  links,
  hasMore,
  loading,
  onLoadMore,
}: MessageListProps): ReactElement {
  const sentinelRef = useRef<HTMLDivElement | null>(null);
  const linkById = new Map(links.map((l) => [l.id, l]));

  useEffect(() => {
    const node = sentinelRef.current;
    // IntersectionObserver isn't part of jsdom; fall back to a no-op there so
    // the unit tests can still mount this component. Tests drive `onLoadMore`
    // directly via the exposed button below.
    if (!node || typeof IntersectionObserver === 'undefined') return;
    if (!hasMore || loading) return;
    const obs = new IntersectionObserver(
      (entries) => {
        for (const e of entries) {
          if (e.isIntersecting) {
            onLoadMore();
            break;
          }
        }
      },
      { rootMargin: '200px' },
    );
    obs.observe(node);
    return () => obs.disconnect();
  }, [hasMore, loading, onLoadMore]);

  return (
    <>
      <ul className="space-y-2">
        {messages.map((m) => (
          <MessageRow key={m.id} message={m} link={linkById.get(m.linkId)} />
        ))}
      </ul>
      {hasMore && (
        <div ref={sentinelRef} className="mt-4 flex justify-center">
          {loading ? (
            <span className="text-xs text-moses-text-muted" aria-live="polite">
              Loading more…
            </span>
          ) : (
            <button
              type="button"
              onClick={onLoadMore}
              className="min-h-[44px] rounded-bento border border-moses-border bg-moses-surface-raised px-4 text-sm font-medium text-moses-text hover:border-moses-accent/60 focus:outline-none focus:ring-2 focus:ring-moses-accent/40 dark:border-moses-border-dark dark:bg-moses-surface-dark-raised dark:text-moses-text-inverse"
            >
              Load more
            </button>
          )}
        </div>
      )}
    </>
  );
}

export default MessageList;

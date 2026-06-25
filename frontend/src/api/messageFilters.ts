// Pure filter helpers for the global Messages page.
//
// These were previously embedded in the now-removed Zustand `messagesStore`.
// They are stateless transforms — the filter object itself is client/UI state
// (lives in component useState), while the fetched-and-paginated message buffer
// is server state owned by TanStack Query (see api/hooks.ts useSearchMessages).
//
// TODO(server-side filters): direction, full-text, and date range are still
// applied client-side after fetch (see bot-api.ts `searchMessages`). When the
// backend grows server-side support, forward them in `toApiParams` directly.

import type { Message, SearchMessagesParams } from '@/lib/bot-api';

export const PAGE_SIZE = 50;

/** Filter shape owned by the Messages page; identical to API params less paging. */
export interface MessagesFilters {
  linkIds: string[];
  direction: 'in' | 'out' | 'all';
  q: string;
  dateFrom: string;
  dateTo: string;
}

export const EMPTY_FILTERS: MessagesFilters = {
  linkIds: [],
  direction: 'all',
  q: '',
  dateFrom: '',
  dateTo: '',
};

/**
 * Apply the parts of the filter that the backend does NOT (yet) accept:
 * direction, full-text, date range. Run after fetch so the user still sees the
 * rows they expect even when the server returns the unfiltered window.
 */
export function applyClientFilters(rows: Message[], f: MessagesFilters): Message[] {
  const q = f.q.trim().toLowerCase();
  const fromMs = f.dateFrom ? new Date(`${f.dateFrom}T00:00:00`).getTime() : -Infinity;
  const toMs = f.dateTo ? new Date(`${f.dateTo}T23:59:59.999`).getTime() : Infinity;
  return rows.filter((m) => {
    if (f.direction !== 'all' && m.direction !== f.direction) return false;
    if (q && !m.text.toLowerCase().includes(q)) return false;
    if (f.linkIds.length > 0 && !f.linkIds.includes(m.linkId)) return false;
    const t = new Date(m.occurredAt).getTime();
    if (Number.isFinite(t)) {
      if (t < fromMs || t > toMs) return false;
    }
    return true;
  });
}

/** Build the API-side request from the user-facing filter object. */
export function toApiParams(
  f: MessagesFilters,
  cursor: string | null,
): SearchMessagesParams {
  return {
    // The backend only accepts a single link_id today. If the user picked
    // exactly one chip we forward it; otherwise we fetch the user-wide window
    // and narrow client-side.
    linkId: f.linkIds.length === 1 ? f.linkIds[0] : undefined,
    direction: f.direction,
    q: f.q || undefined,
    dateFrom: f.dateFrom || undefined,
    dateTo: f.dateTo || undefined,
    limit: PAGE_SIZE,
    cursor: cursor ?? undefined,
  };
}

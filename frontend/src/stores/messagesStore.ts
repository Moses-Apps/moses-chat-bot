// Zustand store for relay-message history.
//
// Two distinct read shapes:
//
//   - fetchMessages(linkId) — single-link tail, used by LinkDetail.
//   - searchAll(filters)    — global Messages page (T-FE-3), with infinite
//     scroll via `loadMore`. The store keeps `pageMessages` separate from the
//     single-link `messages` so the two surfaces don't clobber each other.

import { create } from 'zustand';
import {
  getLinkMessages,
  searchMessages,
  type Message,
  type SearchMessagesParams,
} from '@/lib/bot-api';

const PAGE_SIZE = 50;

/** Filter shape persisted by the Messages page; identical to API params less paging. */
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

export interface MessagesState {
  // Single-link tail (used by LinkDetail).
  messages: Message[];
  loading: boolean;
  error: Error | null;
  fetchMessages: (linkId: string, limit?: number) => Promise<void>;

  // Global search page.
  pageMessages: Message[];
  filters: MessagesFilters;
  cursor: string | null;
  hasMore: boolean;
  searching: boolean;
  searchError: Error | null;
  setFilters: (next: Partial<MessagesFilters>) => void;
  /** Reset cursor + buffer and fetch the first page. */
  searchAll: () => Promise<void>;
  /** Fetch the next page if `hasMore`. No-op while already loading. */
  loadMore: () => Promise<void>;
  /** Test seam: replace search state without touching the network. */
  _setSearchForTest: (next: Partial<Pick<MessagesState,
    'pageMessages' | 'filters' | 'cursor' | 'hasMore' | 'searching' | 'searchError'
  >>) => void;
}

function toError(err: unknown): Error {
  if (err instanceof Error) return err;
  if (typeof err === 'object' && err !== null && 'message' in err) {
    return new Error(String((err as { message: unknown }).message));
  }
  return new Error('Unknown error');
}

/**
 * Apply the parts of the filter that the backend does NOT (yet) accept:
 * direction, full-text, date range. Run after fetch so the user still sees
 * the rows they expect even when the server returns the unfiltered window.
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
function toApiParams(f: MessagesFilters, cursor: string | null): SearchMessagesParams {
  return {
    // The backend only accepts a single link_id today. If the user picked
    // exactly one chip we forward it; otherwise we fetch the user-wide
    // window and narrow client-side.
    linkId: f.linkIds.length === 1 ? f.linkIds[0] : undefined,
    direction: f.direction,
    q: f.q || undefined,
    dateFrom: f.dateFrom || undefined,
    dateTo: f.dateTo || undefined,
    limit: PAGE_SIZE,
    cursor: cursor ?? undefined,
  };
}

export const useMessagesStore = create<MessagesState>((set, get) => ({
  // ─── single-link tail ────────────────────────────────────────────────────
  messages: [],
  loading: false,
  error: null,

  fetchMessages: async (linkId, limit = 100) => {
    set({ loading: true, error: null });
    try {
      const messages = await getLinkMessages(linkId, limit);
      set({ messages, loading: false });
    } catch (err) {
      set({ loading: false, error: toError(err) });
    }
  },

  // ─── global search page ──────────────────────────────────────────────────
  pageMessages: [],
  filters: { ...EMPTY_FILTERS },
  cursor: null,
  hasMore: false,
  searching: false,
  searchError: null,

  setFilters: (next) => {
    set({ filters: { ...get().filters, ...next } });
  },

  searchAll: async () => {
    set({
      searching: true,
      searchError: null,
      pageMessages: [],
      cursor: null,
      hasMore: false,
    });
    try {
      const f = get().filters;
      const res = await searchMessages(toApiParams(f, null));
      const filtered = applyClientFilters(res.messages, f);
      set({
        pageMessages: filtered,
        cursor: res.nextCursor ?? null,
        hasMore: Boolean(res.nextCursor),
        searching: false,
      });
    } catch (err) {
      set({ searching: false, searchError: toError(err) });
    }
  },

  loadMore: async () => {
    const { hasMore, searching, cursor, filters, pageMessages } = get();
    if (!hasMore || searching || !cursor) return;
    set({ searching: true, searchError: null });
    try {
      const res = await searchMessages(toApiParams(filters, cursor));
      const filtered = applyClientFilters(res.messages, filters);
      set({
        pageMessages: [...pageMessages, ...filtered],
        cursor: res.nextCursor ?? null,
        hasMore: Boolean(res.nextCursor),
        searching: false,
      });
    } catch (err) {
      set({ searching: false, searchError: toError(err) });
    }
  },

  _setSearchForTest: (next) => set({ ...next }),
}));

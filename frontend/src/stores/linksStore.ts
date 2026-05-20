// Zustand store for the user's active relay links.
//
// Optimistic semantics: unlink() drops the row from `links` immediately and
// rolls back on failure so the UI feels snappy even on slow networks.

import { create } from 'zustand';
import { deleteLink, listLinks, type Link } from '@/lib/bot-api';
import type { ApiError } from '@/lib/api';

export interface LinksState {
  links: Link[];
  currentLink: Link | null;
  loading: boolean;
  error: Error | null;
  fetchLinks: () => Promise<void>;
  selectLink: (id: string | null) => void;
  unlink: (id: string) => Promise<void>;
  /** Test seam: replace state without touching the network. */
  _setLinksForTest: (links: Link[]) => void;
}

function toError(err: unknown): Error {
  if (err instanceof Error) return err;
  if (typeof err === 'object' && err !== null && 'message' in err) {
    const message = String((err as { message: unknown }).message);
    const code = 'code' in err ? String((err as { code: unknown }).code) : undefined;
    const e = new Error(message);
    if (code) (e as Error & { code?: string }).code = code;
    return e;
  }
  return new Error('Unknown error');
}

export const useLinksStore = create<LinksState>((set, get) => ({
  links: [],
  currentLink: null,
  loading: false,
  error: null,

  fetchLinks: async () => {
    set({ loading: true, error: null });
    try {
      const links = await listLinks();
      set({ links, loading: false });
    } catch (err) {
      set({ loading: false, error: toError(err) });
    }
  },

  selectLink: (id) => {
    const link = id ? get().links.find((l) => l.id === id) ?? null : null;
    set({ currentLink: link });
  },

  unlink: async (id) => {
    const previous = get().links;
    const previousCurrent = get().currentLink;
    // Optimistic removal.
    set({
      links: previous.filter((l) => l.id !== id),
      currentLink: previousCurrent?.id === id ? null : previousCurrent,
      error: null,
    });
    try {
      await deleteLink(id);
    } catch (err) {
      // Roll back.
      set({ links: previous, currentLink: previousCurrent, error: toError(err) });
      throw err as ApiError;
    }
  },

  _setLinksForTest: (links) => set({ links, currentLink: null, error: null, loading: false }),
}));

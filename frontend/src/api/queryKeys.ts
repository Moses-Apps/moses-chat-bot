// Centralized query-key factory — the single source of truth for cache keys.
//
// Reads (useQuery) and writes (useMutation invalidation) MUST reference the
// same key here, so they can never drift. Add a new entry when you add a new
// resource; never inline a string array key in a component.
//
// See FRONTEND_DATA_LAYER.md.
import type { SearchMessagesParams } from '@/lib/bot-api';

export const queryKeys = {
  /** The current viewer's identity (admin gate). */
  viewer: ['viewer'] as const,

  /** Per-tenant Telegram bot connection status. */
  telegramInfo: ['telegram-info'] as const,

  links: {
    /** The user's active relay links. */
    all: ['links'] as const,
  },

  messages: {
    /** Single-link tail (LinkDetail Activity tab). */
    byLink: (linkId: string, limit: number) =>
      ['messages', 'by-link', linkId, limit] as const,
    /** Global paginated search (Messages page, useInfiniteQuery). */
    search: (params: Omit<SearchMessagesParams, 'cursor'>) =>
      ['messages', 'search', params] as const,
  },
};

// Canonical Moses data-layer hooks. See FRONTEND_DATA_LAYER.md.
//
// Components consume these — they NEVER call the axios client directly for
// data-load-on-mount and NEVER load server data in a useEffect. Reads are
// useQuery; writes are useMutation with explicit cache invalidation.
//
// Transport note: this app already ships a configured axios client
// (lib/api.ts) with the Moses CSRF double-submit interceptor + iframe header
// stamping. We wrap the EXISTING typed wrappers in lib/bot-api.ts and
// lib/platform.ts — NOT a new fetch transport. The query layer adds caching,
// dedup, loading/error, and invalidation on top of that transport.
//
// 🚨 Intentionally NOT here: the LinkNew 6-digit-code claim loop. That is a
// real-time imperative pipeline (mint key → poll until claimed/expired →
// revoke), kept fully imperative in pages/LinkNew.tsx. Do not migrate it.

import {
  useQuery,
  useInfiniteQuery,
  useMutation,
  useQueryClient,
} from '@tanstack/react-query';
import { queryKeys } from './queryKeys';
import { toApiParams, type MessagesFilters } from './messageFilters';
import {
  listLinks,
  getLinkMessages,
  searchMessages,
  deleteLink,
  getTelegramInfo,
  connectTelegram,
  disconnectTelegram,
  type Link,
  type SearchMessagesResponse,
} from '@/lib/bot-api';
import { getViewer } from '@/lib/platform';

// ---- Reads -----------------------------------------------------------------

/** The user's active relay links. */
export function useLinks() {
  return useQuery({ queryKey: queryKeys.links.all, queryFn: () => listLinks() });
}

/**
 * Resolve a single link from the cached links list. No extra request — the
 * backend has no `/links/:id` route (it would collide with `/links/codes/:code`
 * in Go's ServeMux), so the detail page reuses the list query.
 */
export function useLink(id: string | undefined) {
  const links = useLinks();
  const link: Link | null =
    id != null ? links.data?.find((l) => l.id === id) ?? null : null;
  return { ...links, link };
}

/** Recent relay history for a single link (LinkDetail Activity tab). */
export function useLinkMessages(linkId: string | undefined, limit = 100) {
  return useQuery({
    queryKey: queryKeys.messages.byLink(linkId ?? '', limit),
    queryFn: () => getLinkMessages(linkId!, limit),
    enabled: !!linkId,
  });
}

/** Per-tenant Telegram bot connection status. */
export function useTelegramInfo(enabled = true) {
  return useQuery({
    queryKey: queryKeys.telegramInfo,
    queryFn: () => getTelegramInfo(),
    enabled,
  });
}

/** The current viewer's identity (admin gate). */
export function useViewer() {
  return useQuery({ queryKey: queryKeys.viewer, queryFn: () => getViewer() });
}

/**
 * Global paginated message search. Cursor pagination via useInfiniteQuery;
 * the client-side direction/text/date narrowing is applied by the component
 * after flattening pages (see api/messageFilters.applyClientFilters).
 *
 * The query key is the API-shaped params *less the cursor* — changing a filter
 * yields a new key and a fresh first page; the cursor only advances pages
 * within one key.
 */
export function useSearchMessages(filters: MessagesFilters) {
  // The cursor varies per page, so it is NOT part of the cache key — only the
  // filter-derived params are (cursor is always null here).
  const keyParams = toApiParams(filters, null);
  delete keyParams.cursor;
  return useInfiniteQuery<SearchMessagesResponse>({
    queryKey: queryKeys.messages.search(keyParams),
    queryFn: ({ pageParam }) =>
      searchMessages(toApiParams(filters, (pageParam as string | null) ?? null)),
    initialPageParam: null as string | null,
    getNextPageParam: (lastPage) => lastPage.nextCursor ?? undefined,
  });
}

// ---- Writes (mutation + invalidation) --------------------------------------

/**
 * Soft-unlink a relay link, then revoke its backing key server-side. Keeps the
 * previous optimistic UX (the row vanishes immediately, rolls back on error)
 * and reconciles with server truth via invalidation on settle.
 */
export function useUnlink() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => deleteLink(id),
    onMutate: async (id: string) => {
      await qc.cancelQueries({ queryKey: queryKeys.links.all });
      const previous = qc.getQueryData<Link[]>(queryKeys.links.all);
      qc.setQueryData<Link[]>(queryKeys.links.all, (old) =>
        old ? old.filter((l) => l.id !== id) : old,
      );
      return { previous };
    },
    onError: (_err, _id, ctx) => {
      if (ctx?.previous) qc.setQueryData(queryKeys.links.all, ctx.previous);
    },
    onSettled: () => qc.invalidateQueries({ queryKey: queryKeys.links.all }),
  });
}

/** Connect the tenant's Telegram bot (tenant-admin only). */
export function useConnectTelegram() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (token: string) => connectTelegram(token),
    onSuccess: (info) => {
      qc.setQueryData(queryKeys.telegramInfo, info);
    },
    onSettled: () => qc.invalidateQueries({ queryKey: queryKeys.telegramInfo }),
  });
}

/** Disconnect the tenant's Telegram bot (tenant-admin only). */
export function useDisconnectTelegram() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => disconnectTelegram(),
    onSuccess: () => {
      qc.setQueryData(queryKeys.telegramInfo, { configured: false });
    },
    onSettled: () => qc.invalidateQueries({ queryKey: queryKeys.telegramInfo }),
  });
}

import { QueryClient } from '@tanstack/react-query';

// Canonical Moses query client (shared singleton). See FRONTEND_DATA_LAYER.md.
//
// Defaults tuned for a deployed Moses app:
//   - staleTime 30s: data stays fresh across remounts/route changes, so
//     navigating back to a page doesn't trigger a refetch storm.
//   - retry 1: one retry on transient failures, then surface the error.
//   - refetchOnWindowFocus off: Moses apps render inside iframes (embed mode,
//     Tauri); focus churn there is noisy and not a useful refetch trigger.
//
// Real-time freshness is driven by explicit cache invalidation (mutations in
// hooks.ts), NOT by focus/interval polling. The one genuinely real-time surface
// in this app — the LinkNew 6-digit-code claim poll — stays fully imperative
// (a setInterval loop in pages/LinkNew.tsx) and is intentionally NOT a query.
export const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 30_000,
      retry: 1,
      refetchOnWindowFocus: false,
    },
  },
});

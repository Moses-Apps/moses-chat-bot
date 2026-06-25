// Test helper: a fresh QueryClient per render so cache never leaks across
// tests, with retries disabled (so a mocked rejection surfaces immediately
// instead of being retried) and a zero staleTime.

import type { ReactElement, ReactNode } from 'react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';

export function makeTestQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: { retry: false, staleTime: 0, gcTime: 0 },
      mutations: { retry: false },
    },
  });
}

/** Wrap children in a fresh QueryClientProvider for a single test render. */
export function withQueryClient(children: ReactNode): ReactElement {
  return (
    <QueryClientProvider client={makeTestQueryClient()}>
      {children}
    </QueryClientProvider>
  );
}

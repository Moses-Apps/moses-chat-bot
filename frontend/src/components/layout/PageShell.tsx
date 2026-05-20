// Outer layout: top header (app name + breadcrumb + connection status indicator)
// and a main content slot with Bento-grid-style padding.
//
// The connection-status indicator uses a derived heuristic: any unhandled
// fetch error within the next ~30s could flip it; for T-FE-1 we just expose
// the slot via StatusBadge so T-FE-2 can wire the real state.

import type { ReactNode, ReactElement } from 'react';
import { Link } from 'react-router-dom';
import StatusBadge from '@/components/StatusBadge';
import Breadcrumb from './Breadcrumb';
import { isEmbedded } from '@/lib/iframe';

interface PageShellProps {
  children: ReactNode;
}

export function PageShell({ children }: PageShellProps): ReactElement {
  const embedded = isEmbedded();
  return (
    <div className="flex min-h-full flex-col bg-moses-surface text-moses-text dark:bg-moses-surface-dark dark:text-moses-text-inverse">
      <a
        href="#main"
        className="sr-only focus:not-sr-only focus:fixed focus:left-4 focus:top-4 focus:z-50 focus:rounded focus:bg-moses-accent focus:px-3 focus:py-2 focus:text-white"
      >
        Skip to content
      </a>
      <header
        role="banner"
        className="sticky top-0 z-10 border-b border-moses-border bg-moses-surface-raised/95 backdrop-blur dark:border-moses-border-dark dark:bg-moses-surface-dark-raised/95"
      >
        <div className="mx-auto flex max-w-6xl items-center justify-between gap-4 px-4 py-3">
          <div className="flex items-center gap-4">
            <Link
              to="/"
              className="text-base font-semibold tracking-tight text-moses-text hover:text-moses-accent dark:text-moses-text-inverse"
            >
              Chat Bot Bridge
            </Link>
            <span aria-hidden="true" className="text-moses-text-subtle">
              ·
            </span>
            <Breadcrumb />
          </div>
          <StatusBadge
            status={embedded ? 'active' : 'pending'}
            label={embedded ? 'Connected to Moses' : 'Standalone'}
          />
        </div>
      </header>
      <main id="main" tabIndex={-1} role="main" className="flex-1 focus:outline-none">
        <div className="mx-auto max-w-6xl px-4 py-6">{children}</div>
      </main>
    </div>
  );
}

export default PageShell;

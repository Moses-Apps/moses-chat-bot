// Dashboard — entry page for the linking UI.
//
// Bento grid:
//   - "My active links" (full width on small, 2-of-3 on lg+) showing one
//     LinkCard per active link, with empty / loading / error states.
//   - "Link new chat" (1-of-3 on lg+) — prominent CTA → /link/new.

import { useEffect, type ReactElement } from 'react';
import { Link as RouterLink } from 'react-router-dom';

import BentoCard from '@/components/layout/BentoCard';
import LinkCard from '@/components/links/LinkCard';
import { useLinksStore } from '@/stores/linksStore';

function SkeletonRow(): ReactElement {
  return (
    <div
      aria-hidden="true"
      className="flex min-h-[64px] items-center gap-4 rounded-bento border border-moses-border bg-moses-surface-raised p-4 dark:border-moses-border-dark dark:bg-moses-surface-dark-raised"
    >
      <span className="h-10 w-10 shrink-0 animate-pulse rounded-full bg-moses-surface-sunken dark:bg-moses-surface-dark-sunken" />
      <span className="flex-1 space-y-2">
        <span className="block h-3 w-32 animate-pulse rounded bg-moses-surface-sunken dark:bg-moses-surface-dark-sunken" />
        <span className="block h-3 w-48 animate-pulse rounded bg-moses-surface-sunken dark:bg-moses-surface-dark-sunken" />
      </span>
    </div>
  );
}

function EmptyState(): ReactElement {
  return (
    <div className="flex flex-col items-center gap-3 rounded-bento border border-dashed border-moses-border p-8 text-center dark:border-moses-border-dark">
      <svg
        viewBox="0 0 48 48"
        className="h-12 w-12 text-moses-accent"
        aria-hidden="true"
      >
        <path
          fill="currentColor"
          d="M24 4a20 20 0 1 0 0 40 20 20 0 0 0 0-40m0 6a14 14 0 1 1 0 28 14 14 0 0 1 0-28m-1 4v8h-8v4h8v8h4v-8h8v-4h-8v-8z"
        />
      </svg>
      <h3 className="text-base font-semibold">Connect your first chat</h3>
      <p className="max-w-sm text-sm text-moses-text-muted">
        Link Telegram (and soon Discord, Slack, WhatsApp) to message your Moses
        agents from anywhere.
      </p>
      <RouterLink
        to="/link/new"
        className="mt-2 inline-flex min-h-[44px] items-center rounded-bento bg-moses-accent px-4 text-sm font-semibold text-white hover:bg-moses-accent-hover focus:outline-none focus:ring-2 focus:ring-moses-accent/40"
      >
        Link a chat
      </RouterLink>
    </div>
  );
}

export default function Dashboard(): ReactElement {
  const links = useLinksStore((s) => s.links);
  const loading = useLinksStore((s) => s.loading);
  const error = useLinksStore((s) => s.error);
  const fetchLinks = useLinksStore((s) => s.fetchLinks);

  useEffect(() => {
    void fetchLinks();
  }, [fetchLinks]);

  return (
    <div className="grid grid-cols-1 gap-4 lg:grid-cols-3">
      <BentoCard
        title="My active links"
        subtitle="Chats wired to your Moses agents"
        className="lg:col-span-2"
      >
        {loading && links.length === 0 ? (
          <div className="space-y-3" aria-busy="true" aria-live="polite">
            <SkeletonRow />
            <SkeletonRow />
            <SkeletonRow />
          </div>
        ) : error ? (
          <div
            role="alert"
            className="flex flex-col gap-3 rounded-bento border border-moses-status-error/40 bg-moses-status-error/10 p-4 sm:flex-row sm:items-center sm:justify-between"
          >
            <p className="text-sm text-moses-status-error">
              Could not load links: {error.message}
            </p>
            <button
              type="button"
              onClick={() => void fetchLinks()}
              className="min-h-[44px] rounded-bento border border-moses-status-error/50 bg-moses-surface-raised px-4 text-sm font-medium text-moses-status-error hover:bg-moses-status-error/10 focus:outline-none focus:ring-2 focus:ring-moses-status-error/40 dark:bg-moses-surface-dark-raised"
            >
              Retry
            </button>
          </div>
        ) : links.length === 0 ? (
          <EmptyState />
        ) : (
          <ul className="space-y-3">
            {links.map((link) => (
              <li key={link.id}>
                <LinkCard link={link} />
              </li>
            ))}
          </ul>
        )}
      </BentoCard>

      <BentoCard
        title="Link a new chat"
        subtitle="Generate a 6-digit code to claim from Telegram"
      >
        <div className="flex flex-col items-start gap-4">
          <p className="text-sm text-moses-text-muted">
            Talk to Moses from Telegram with your own Moses identity. We never
            store your AI provider tokens; the bot uses a per-user MCP key
            scoped to you.
          </p>
          <RouterLink
            to="/link/new"
            className="inline-flex min-h-[44px] items-center rounded-bento bg-moses-accent px-4 text-sm font-semibold text-white hover:bg-moses-accent-hover focus:outline-none focus:ring-2 focus:ring-moses-accent/40"
          >
            Link new chat
          </RouterLink>
        </div>
      </BentoCard>
    </div>
  );
}

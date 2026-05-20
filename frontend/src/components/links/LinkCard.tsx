// Single-link summary tile shown on the dashboard.
//
// Layout (sm+): provider icon | identifiers | status badge | last-used.
// On <sm screens it collapses to a stacked layout so the touch target
// (the wrapping <Link>) stays >=44px tall.

import type { ReactElement } from 'react';
import { Link as RouterLink } from 'react-router-dom';
import type { Link } from '@/lib/bot-api';
import StatusBadge from '@/components/StatusBadge';
import ProviderIcon from './ProviderIcon';

interface LinkCardProps {
  link: Link;
  /** Optional: override Date.now() in tests for deterministic relative time. */
  now?: () => number;
}

function providerLabel(provider: string): string {
  if (provider === 'telegram') return 'Telegram';
  return provider.charAt(0).toUpperCase() + provider.slice(1);
}

const relativeFormatter = new Intl.RelativeTimeFormat(undefined, { numeric: 'auto' });
const absoluteFormatter = new Intl.DateTimeFormat(undefined, {
  dateStyle: 'medium',
  timeStyle: 'short',
});

function formatLastUsed(iso: string | null | undefined, now: () => number): string {
  if (!iso) return 'Never used yet';
  const t = new Date(iso).getTime();
  if (Number.isNaN(t)) return 'Unknown';
  const diffSec = Math.round((t - now()) / 1000);
  const abs = Math.abs(diffSec);
  if (abs < 60) return relativeFormatter.format(diffSec, 'second');
  if (abs < 3600) return relativeFormatter.format(Math.round(diffSec / 60), 'minute');
  if (abs < 86400) return relativeFormatter.format(Math.round(diffSec / 3600), 'hour');
  if (abs < 86400 * 30) return relativeFormatter.format(Math.round(diffSec / 86400), 'day');
  return absoluteFormatter.format(t);
}

export function LinkCard({ link, now = Date.now }: LinkCardProps): ReactElement {
  const status = link.isActive ? 'active' : 'inactive';
  const lastUsed = formatLastUsed(link.lastUsedAt ?? null, now);
  const display = link.providerDisplayName ?? link.providerUserId;

  return (
    <RouterLink
      to={`/links/${link.id}`}
      aria-label={`Open ${providerLabel(link.provider)} link for ${display}`}
      className="flex min-h-[64px] items-start gap-4 rounded-bento border border-moses-border bg-moses-surface-raised p-4 transition-shadow hover:shadow-bento-hover focus:outline-none focus:ring-2 focus:ring-moses-accent/40 sm:items-center dark:border-moses-border-dark dark:bg-moses-surface-dark-raised"
    >
      <span className="flex h-10 w-10 shrink-0 items-center justify-center rounded-full bg-moses-accent-soft text-moses-accent">
        <ProviderIcon provider={link.provider} className="h-5 w-5" />
      </span>
      <div className="flex flex-1 flex-col gap-1 sm:flex-row sm:items-center sm:justify-between sm:gap-4">
        <div className="min-w-0">
          <p className="truncate text-sm font-semibold text-moses-text">
            {providerLabel(link.provider)}
          </p>
          <p className="truncate text-xs text-moses-text-muted">{display}</p>
        </div>
        <div className="flex items-center gap-3">
          <StatusBadge status={status} />
          <span className="text-xs text-moses-text-muted">{lastUsed}</span>
        </div>
      </div>
    </RouterLink>
  );
}

export default LinkCard;

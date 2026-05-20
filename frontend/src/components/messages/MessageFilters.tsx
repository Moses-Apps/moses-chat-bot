// Filter bar for the Messages page.
//
// Layout:
//   - sm+: search input + chip multi-select (links) + direction segmented
//     control + two date inputs in a single sticky row.
//   - <sm: search input stays prominent; the rest collapse into a <details>
//     accordion so the touch surface stays sane on a 320px viewport.

import { useId, type ReactElement } from 'react';
import type { Link } from '@/lib/bot-api';
import SearchInput from '@/components/SearchInput';
import ProviderIcon from '@/components/links/ProviderIcon';
import type { MessagesFilters } from '@/stores/messagesStore';

interface MessageFiltersProps {
  links: Link[];
  filters: MessagesFilters;
  /** Patch a subset of the filter object; parent owns persistence. */
  onChange: (patch: Partial<MessagesFilters>) => void;
}

function chipLabel(link: Link): string {
  const provider = link.provider === 'telegram' ? 'Telegram' : link.provider;
  return `${provider}: ${link.providerDisplayName ?? link.providerUserId}`;
}

export function MessageFilters({
  links,
  filters,
  onChange,
}: MessageFiltersProps): ReactElement {
  const fromId = useId();
  const toId = useId();
  const dirId = useId();

  function toggleLink(id: string): void {
    const has = filters.linkIds.includes(id);
    onChange({
      linkIds: has
        ? filters.linkIds.filter((x) => x !== id)
        : [...filters.linkIds, id],
    });
  }

  const directionControl = (
    <fieldset className="flex items-center gap-2" aria-labelledby={dirId}>
      <legend id={dirId} className="sr-only">
        Direction
      </legend>
      {(['all', 'in', 'out'] as const).map((d) => {
        const active = filters.direction === d;
        return (
          <button
            key={d}
            type="button"
            aria-pressed={active}
            onClick={() => onChange({ direction: d })}
            className={[
              'min-h-[44px] rounded-bento border px-3 text-sm font-medium transition-colors',
              active
                ? 'border-moses-accent bg-moses-accent text-white'
                : 'border-moses-border bg-moses-surface-raised text-moses-text hover:border-moses-accent/60 dark:border-moses-border-dark dark:bg-moses-surface-dark-raised dark:text-moses-text-inverse',
            ].join(' ')}
          >
            {d === 'all' ? 'All' : d === 'in' ? 'Inbound' : 'Outbound'}
          </button>
        );
      })}
    </fieldset>
  );

  const dateControls = (
    <div className="flex flex-wrap items-center gap-2">
      <label htmlFor={fromId} className="text-xs text-moses-text-muted">
        From
      </label>
      <input
        id={fromId}
        type="date"
        aria-label="From date"
        value={filters.dateFrom}
        onChange={(e) => onChange({ dateFrom: e.target.value })}
        className="min-h-[44px] rounded-bento border border-moses-border bg-moses-surface-raised px-2 text-sm text-moses-text focus:border-moses-accent focus:outline-none focus:ring-2 focus:ring-moses-accent/30 dark:border-moses-border-dark dark:bg-moses-surface-dark-raised dark:text-moses-text-inverse"
      />
      <label htmlFor={toId} className="text-xs text-moses-text-muted">
        To
      </label>
      <input
        id={toId}
        type="date"
        aria-label="To date"
        value={filters.dateTo}
        onChange={(e) => onChange({ dateTo: e.target.value })}
        className="min-h-[44px] rounded-bento border border-moses-border bg-moses-surface-raised px-2 text-sm text-moses-text focus:border-moses-accent focus:outline-none focus:ring-2 focus:ring-moses-accent/30 dark:border-moses-border-dark dark:bg-moses-surface-dark-raised dark:text-moses-text-inverse"
      />
    </div>
  );

  const linkChips = links.length === 0 ? null : (
    <fieldset className="flex flex-wrap gap-2" aria-label="Filter by link">
      {links.map((link) => {
        const active = filters.linkIds.includes(link.id);
        return (
          <button
            key={link.id}
            type="button"
            aria-pressed={active}
            onClick={() => toggleLink(link.id)}
            className={[
              'inline-flex min-h-[44px] items-center gap-2 rounded-full border px-3 text-xs font-medium transition-colors',
              active
                ? 'border-moses-accent bg-moses-accent-soft text-moses-accent'
                : 'border-moses-border bg-moses-surface-raised text-moses-text-muted hover:border-moses-accent/60 dark:border-moses-border-dark dark:bg-moses-surface-dark-raised',
            ].join(' ')}
          >
            <ProviderIcon provider={link.provider} className="h-3.5 w-3.5" />
            <span className="truncate max-w-[12rem]">{chipLabel(link)}</span>
          </button>
        );
      })}
    </fieldset>
  );

  return (
    <div className="sticky top-16 z-10 mb-4 space-y-3 rounded-bento border border-moses-border bg-moses-surface-raised p-3 shadow-bento dark:border-moses-border-dark dark:bg-moses-surface-dark-raised">
      <SearchInput
        value={filters.q}
        onChange={(q) => onChange({ q })}
        ariaLabel="Search message text"
        placeholder="Search messages…"
      />
      {/* sm+: inline */}
      <div className="hidden flex-wrap items-center gap-4 sm:flex">
        {directionControl}
        {dateControls}
      </div>
      {linkChips && <div className="hidden sm:block">{linkChips}</div>}
      {/* <sm: accordion */}
      <details className="sm:hidden">
        <summary className="cursor-pointer min-h-[44px] py-2 text-sm font-medium text-moses-accent">
          More filters
        </summary>
        <div className="mt-3 space-y-3">
          {directionControl}
          {dateControls}
          {linkChips}
        </div>
      </details>
    </div>
  );
}

export default MessageFilters;

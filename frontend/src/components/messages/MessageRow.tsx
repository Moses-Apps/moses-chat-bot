// One row in the global Messages search list.
//
// - Collapsed: direction icon, provider+chat label, truncated text (200 chars),
//   relative timestamp, expand chevron.
// - Expanded: full text + metadata (error, message id, raw timestamps).
// - Single <button> so the entire row is keyboard-focusable.

import { useId, useState, type ReactElement } from 'react';
import type { Link, Message } from '@/lib/bot-api';
import ProviderIcon from '@/components/links/ProviderIcon';

const relativeFormatter = new Intl.RelativeTimeFormat(undefined, { numeric: 'auto' });
const absoluteFormatter = new Intl.DateTimeFormat(undefined, {
  dateStyle: 'medium',
  timeStyle: 'short',
});

function providerLabel(provider: string): string {
  if (provider === 'telegram') return 'Telegram';
  return provider.charAt(0).toUpperCase() + provider.slice(1);
}

function formatRelative(iso: string, now: () => number): string {
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

interface MessageRowProps {
  message: Message;
  /** Resolved link metadata for the provider+chat label. May be missing if the link was unlinked. */
  link?: Link;
  /** Test seam for deterministic relative-time. */
  now?: () => number;
}

export function MessageRow({ message, link, now = Date.now }: MessageRowProps): ReactElement {
  const [expanded, setExpanded] = useState(false);
  const baseId = useId();
  const direction = message.direction === 'in' ? 'In' : 'Out';
  const provider = link?.provider ?? 'unknown';
  const label = link
    ? `${providerLabel(provider)} · ${link.providerDisplayName ?? link.providerUserId}`
    : 'Unlinked chat';
  const truncated =
    message.text.length > 200 ? `${message.text.slice(0, 200)}…` : message.text;
  const rel = formatRelative(message.occurredAt, now);
  const detailId = `${baseId}-detail`;

  return (
    <li>
      <button
        type="button"
        aria-expanded={expanded}
        aria-controls={detailId}
        onClick={() => setExpanded(!expanded)}
        className="flex w-full min-h-[44px] items-start gap-3 rounded-bento border border-moses-border bg-moses-surface-raised p-3 text-left hover:border-moses-accent/60 focus:outline-none focus:ring-2 focus:ring-moses-accent/40 dark:border-moses-border-dark dark:bg-moses-surface-dark-raised"
      >
        <span
          aria-hidden="true"
          className={[
            'mt-0.5 inline-flex h-6 w-6 shrink-0 items-center justify-center rounded-full text-xs font-semibold',
            message.direction === 'in'
              ? 'bg-moses-accent-soft text-moses-accent'
              : 'bg-moses-status-active/15 text-moses-status-active',
          ].join(' ')}
        >
          {message.direction === 'in' ? '↓' : '↑'}
        </span>
        <span className="flex flex-1 min-w-0 flex-col gap-1">
          <span className="flex items-center gap-2 text-xs text-moses-text-muted">
            <ProviderIcon provider={provider} className="h-3.5 w-3.5" />
            <span className="truncate">{label}</span>
            <span aria-hidden="true">·</span>
            <span className="whitespace-nowrap">{rel}</span>
          </span>
          <span className="sr-only">{direction} message: </span>
          <span className="block text-sm text-moses-text dark:text-moses-text-inverse">
            {truncated}
          </span>
          {message.error && (
            <span className="text-xs text-moses-status-error">{message.error}</span>
          )}
        </span>
        <span
          aria-hidden="true"
          className={[
            'ml-2 mt-1 inline-block text-moses-text-subtle transition-transform',
            expanded ? 'rotate-180' : '',
          ].join(' ')}
        >
          ▾
        </span>
      </button>
      {expanded && (
        <div
          id={detailId}
          className="mt-2 rounded-bento border border-moses-border bg-moses-surface p-3 text-sm dark:border-moses-border-dark dark:bg-moses-surface-dark"
        >
          <p className="whitespace-pre-wrap text-moses-text dark:text-moses-text-inverse">
            {message.text}
          </p>
          <dl className="mt-3 grid grid-cols-[max-content_1fr] gap-x-3 gap-y-1 text-xs text-moses-text-muted">
            <dt>Direction</dt>
            <dd>{direction}</dd>
            <dt>When</dt>
            <dd>{new Date(message.occurredAt).toLocaleString()}</dd>
            <dt>Message id</dt>
            <dd className="break-all font-mono">{message.id}</dd>
            <dt>Link id</dt>
            <dd className="break-all font-mono">{message.linkId}</dd>
            {message.metadata &&
              Object.entries(message.metadata).map(([k, v]) => (
                <span key={k} className="contents">
                  <dt>{k}</dt>
                  <dd className="break-words">{JSON.stringify(v)}</dd>
                </span>
              ))}
            {message.error && (
              <>
                <dt>Error</dt>
                <dd className="text-moses-status-error">{message.error}</dd>
              </>
            )}
          </dl>
        </div>
      )}
    </li>
  );
}

export default MessageRow;

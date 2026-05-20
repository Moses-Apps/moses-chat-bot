// Renders relay messages grouped by date.
//
// - Each row uses a <button> so the keyboard can expand it.
// - The full text + metadata appear in an aria-controlled region below.
// - Long-form bodies are clamped via Tailwind's `line-clamp` utility (built-in
//   to v3.3+), keeping the row a single line in collapsed state.

import { useMemo, useState, type ReactElement } from 'react';
import type { Message } from '@/lib/bot-api';

interface MessageListProps {
  messages: Message[];
}

const dateGroupFormatter = new Intl.DateTimeFormat(undefined, {
  dateStyle: 'long',
});
const timeFormatter = new Intl.DateTimeFormat(undefined, {
  timeStyle: 'short',
});

function groupByDate(
  messages: Message[],
): Array<{ label: string; items: Message[] }> {
  const groups = new Map<string, Message[]>();
  // Sort newest-first then group by local-date label.
  const sorted = [...messages].sort(
    (a, b) => new Date(b.occurredAt).getTime() - new Date(a.occurredAt).getTime(),
  );
  for (const m of sorted) {
    const t = new Date(m.occurredAt);
    const key = Number.isNaN(t.getTime()) ? 'Unknown' : dateGroupFormatter.format(t);
    if (!groups.has(key)) groups.set(key, []);
    groups.get(key)!.push(m);
  }
  return Array.from(groups.entries()).map(([label, items]) => ({ label, items }));
}

export function MessageList({ messages }: MessageListProps): ReactElement {
  const [expanded, setExpanded] = useState<string | null>(null);
  const grouped = useMemo(() => groupByDate(messages), [messages]);

  if (messages.length === 0) {
    return (
      <p className="rounded-bento border border-dashed border-moses-border p-6 text-center text-sm text-moses-text-muted dark:border-moses-border-dark">
        No messages yet. Send a message in your chat to see it relayed here.
      </p>
    );
  }

  return (
    <div className="space-y-6">
      {grouped.map((g) => (
        <section key={g.label} aria-labelledby={`msg-group-${g.label}`}>
          <h3
            id={`msg-group-${g.label}`}
            className="mb-2 text-xs font-semibold uppercase tracking-wide text-moses-text-muted"
          >
            {g.label}
          </h3>
          <ul className="space-y-2">
            {g.items.map((m) => {
              const isExpanded = expanded === m.id;
              const direction = m.direction === 'in' ? 'In' : 'Out';
              const time = (() => {
                const t = new Date(m.occurredAt);
                return Number.isNaN(t.getTime()) ? '' : timeFormatter.format(t);
              })();
              return (
                <li key={m.id}>
                  <button
                    type="button"
                    onClick={() => setExpanded(isExpanded ? null : m.id)}
                    aria-expanded={isExpanded}
                    aria-controls={`msg-detail-${m.id}`}
                    className="flex w-full min-h-[44px] items-start gap-3 rounded-bento border border-moses-border bg-moses-surface-raised p-3 text-left hover:border-moses-accent/60 focus:outline-none focus:ring-2 focus:ring-moses-accent/40 dark:border-moses-border-dark dark:bg-moses-surface-dark-raised"
                  >
                    <span
                      aria-hidden="true"
                      className={[
                        'mt-0.5 inline-flex h-6 w-6 shrink-0 items-center justify-center rounded-full text-xs font-semibold',
                        m.direction === 'in'
                          ? 'bg-moses-accent-soft text-moses-accent'
                          : 'bg-moses-status-active/15 text-moses-status-active',
                      ].join(' ')}
                    >
                      {m.direction === 'in' ? '↓' : '↑'}
                    </span>
                    <span className="flex-1 min-w-0">
                      <span className="sr-only">{direction} message: </span>
                      <span className="block truncate text-sm text-moses-text">
                        {m.text}
                      </span>
                      {m.error && (
                        <span className="mt-1 block text-xs text-moses-status-error">
                          {m.error}
                        </span>
                      )}
                    </span>
                    <span className="ml-2 text-xs text-moses-text-muted">{time}</span>
                  </button>
                  {isExpanded && (
                    <div
                      id={`msg-detail-${m.id}`}
                      className="mt-2 rounded-bento border border-moses-border bg-moses-surface p-3 text-sm dark:border-moses-border-dark dark:bg-moses-surface-dark"
                    >
                      <p className="whitespace-pre-wrap text-moses-text">{m.text}</p>
                      <dl className="mt-3 grid grid-cols-[max-content_1fr] gap-x-3 gap-y-1 text-xs text-moses-text-muted">
                        <dt>Direction</dt>
                        <dd>{direction}</dd>
                        <dt>When</dt>
                        <dd>{new Date(m.occurredAt).toLocaleString()}</dd>
                        {m.metadata &&
                          Object.entries(m.metadata).map(([k, v]) => (
                            <span key={k} className="contents">
                              <dt>{k}</dt>
                              <dd className="break-words">{JSON.stringify(v)}</dd>
                            </span>
                          ))}
                      </dl>
                    </div>
                  )}
                </li>
              );
            })}
          </ul>
        </section>
      ))}
    </div>
  );
}

export default MessageList;

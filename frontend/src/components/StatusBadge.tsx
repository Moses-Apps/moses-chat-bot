// Colored pill for {active|inactive|pending|error} status.
//
// Color tokens come from tailwind.config.cjs `moses.status.*`. The default
// label matches the status name so callers can omit it.

import type { ReactElement } from 'react';

export type StatusKind = 'active' | 'inactive' | 'pending' | 'error';

interface StatusBadgeProps {
  status: StatusKind;
  label?: string;
  className?: string;
}

const STYLES: Record<StatusKind, string> = {
  // Background uses /15 alpha for legibility; text uses the full status color.
  // Border kept subtle so the badge reads as a pill, not a button.
  active:
    'bg-moses-status-active/15 text-moses-status-active border-moses-status-active/30',
  inactive:
    'bg-moses-status-inactive/15 text-moses-status-inactive border-moses-status-inactive/30',
  pending:
    'bg-moses-status-pending/15 text-moses-status-pending border-moses-status-pending/30',
  error:
    'bg-moses-status-error/15 text-moses-status-error border-moses-status-error/30',
};

const DEFAULT_LABEL: Record<StatusKind, string> = {
  active: 'Active',
  inactive: 'Inactive',
  pending: 'Pending',
  error: 'Error',
};

export function StatusBadge({ status, label, className }: StatusBadgeProps): ReactElement {
  const text = label ?? DEFAULT_LABEL[status];
  return (
    <span
      role="status"
      aria-label={text}
      className={[
        'inline-flex items-center gap-1 rounded-full border px-2 py-0.5 text-xs font-medium',
        STYLES[status],
        className ?? '',
      ]
        .filter(Boolean)
        .join(' ')}
    >
      <span
        aria-hidden="true"
        className="inline-block size-1.5 rounded-full bg-current"
      />
      {text}
    </span>
  );
}

export default StatusBadge;

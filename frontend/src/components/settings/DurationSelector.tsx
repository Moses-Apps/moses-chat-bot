// Quick-pick durations for "Snooze now".
//
// Four buttons: 30m / 1h / 4h / until-morning. Selecting one calls
// `onSelect(durationMs)`. Picking "Clear" calls `onSelect(0)`.

import { useMemo, type ReactElement } from 'react';

const MINUTE = 60_000;
const HOUR = 60 * MINUTE;

interface DurationSelectorProps {
  /** Receives ms-from-now; 0 = clear. */
  onSelect: (durationMs: number) => void;
  /** Optional: drive the "until tomorrow morning" calculation from a fixed clock. */
  now?: () => Date;
}

interface Choice {
  label: string;
  durationMs: number;
}

function untilTomorrowMorningMs(now: Date): number {
  // 8am local time the next calendar day.
  const target = new Date(now);
  target.setHours(8, 0, 0, 0);
  if (target.getTime() <= now.getTime()) {
    target.setDate(target.getDate() + 1);
  } else {
    // Today is still before 8am — snooze until today's 8am.
  }
  return Math.max(0, target.getTime() - now.getTime());
}

export function DurationSelector({
  onSelect,
  now = () => new Date(),
}: DurationSelectorProps): ReactElement {
  const choices = useMemo<Choice[]>(() => {
    return [
      { label: '30 min', durationMs: 30 * MINUTE },
      { label: '1 hour', durationMs: HOUR },
      { label: '4 hours', durationMs: 4 * HOUR },
      { label: 'Until 8am', durationMs: untilTomorrowMorningMs(now()) },
    ];
  }, [now]);

  return (
    <div className="flex flex-wrap gap-2">
      {choices.map((c) => (
        <button
          key={c.label}
          type="button"
          onClick={() => onSelect(c.durationMs)}
          className="min-h-[44px] rounded-bento border border-moses-border bg-moses-surface-raised px-3 text-sm font-medium text-moses-text hover:border-moses-accent/60 focus:outline-none focus:ring-2 focus:ring-moses-accent/40 dark:border-moses-border-dark dark:bg-moses-surface-dark-raised dark:text-moses-text-inverse"
        >
          {c.label}
        </button>
      ))}
      <button
        type="button"
        onClick={() => onSelect(0)}
        className="min-h-[44px] rounded-bento border border-moses-border bg-moses-surface px-3 text-sm font-medium text-moses-text-muted hover:text-moses-text focus:outline-none focus:ring-2 focus:ring-moses-accent/40 dark:border-moses-border-dark dark:bg-moses-surface-dark dark:hover:text-moses-text-inverse"
      >
        Clear
      </button>
    </div>
  );
}

export default DurationSelector;

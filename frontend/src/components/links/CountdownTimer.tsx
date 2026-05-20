// Counts down from `expiresAt` ISO timestamp to 0 and fires `onExpired` once.
//
// - Updates every 1s on a setInterval; cleans up on unmount or expiry.
// - Announces remaining time politely via aria-live="polite".
// - Format: mm:ss (zero-padded). Negative clamps to "00:00".

import { useEffect, useRef, useState, type ReactElement } from 'react';

interface CountdownTimerProps {
  /** ISO-8601 timestamp when the countdown reaches zero. */
  expiresAt: string;
  onExpired?: () => void;
  /** Visible label prefix (e.g. "Code expires in"). */
  label?: string;
  /** Test seam: override Date.now(). */
  now?: () => number;
}

function remainingMs(expiresAt: string, now: () => number): number {
  const target = new Date(expiresAt).getTime();
  if (Number.isNaN(target)) return 0;
  return Math.max(0, target - now());
}

function format(ms: number): string {
  const totalSec = Math.ceil(ms / 1000);
  const mm = Math.floor(totalSec / 60)
    .toString()
    .padStart(2, '0');
  const ss = (totalSec % 60).toString().padStart(2, '0');
  return `${mm}:${ss}`;
}

export function CountdownTimer({
  expiresAt,
  onExpired,
  label = 'Expires in',
  now = Date.now,
}: CountdownTimerProps): ReactElement {
  const [ms, setMs] = useState<number>(() => remainingMs(expiresAt, now));
  const firedRef = useRef(false);

  useEffect(() => {
    firedRef.current = false;
    setMs(remainingMs(expiresAt, now));
    const id = window.setInterval(() => {
      const next = remainingMs(expiresAt, now);
      setMs(next);
      if (next <= 0 && !firedRef.current) {
        firedRef.current = true;
        onExpired?.();
        window.clearInterval(id);
      }
    }, 1000);
    return () => window.clearInterval(id);
  }, [expiresAt, onExpired, now]);

  const text = format(ms);
  return (
    <p className="text-sm text-moses-text-muted" aria-live="polite">
      <span>{label} </span>
      <span className="font-mono tabular-nums text-moses-text" data-testid="countdown-value">
        {text}
      </span>
    </p>
  );
}

export default CountdownTimer;

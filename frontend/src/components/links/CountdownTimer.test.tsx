// CountdownTimer ticks + onExpired callback verification.

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { act, render, screen } from '@testing-library/react';
import CountdownTimer from './CountdownTimer';

describe('<CountdownTimer />', () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it('counts down each second to mm:ss format', async () => {
    vi.setSystemTime(new Date('2026-05-19T12:00:00Z'));
    render(<CountdownTimer expiresAt="2026-05-19T12:01:00Z" />);
    expect(screen.getByTestId('countdown-value').textContent).toBe('01:00');

    // Advance 1s: time -> 12:00:01, remaining = 59000ms -> "00:59".
    await act(async () => {
      vi.advanceTimersByTime(1000);
    });
    expect(screen.getByTestId('countdown-value').textContent).toBe('00:59');

    // Advance 30s more: time -> 12:00:31, remaining = 29000ms -> "00:29".
    await act(async () => {
      vi.advanceTimersByTime(30_000);
    });
    expect(screen.getByTestId('countdown-value').textContent).toBe('00:29');
  });

  it('fires onExpired exactly once when the timer hits zero', async () => {
    vi.setSystemTime(new Date('2026-05-19T12:00:00Z'));
    const onExpired = vi.fn();
    render(<CountdownTimer expiresAt="2026-05-19T12:00:02Z" onExpired={onExpired} />);

    // Two 1s ticks brings us to 12:00:02 — remaining hits 0.
    await act(async () => {
      vi.advanceTimersByTime(2000);
    });
    // One more tick after expiry to ensure the callback fires once (and the
    // interval is cleared so further ticks don't accumulate calls).
    await act(async () => {
      vi.advanceTimersByTime(2000);
    });
    expect(onExpired).toHaveBeenCalledTimes(1);
    expect(screen.getByTestId('countdown-value').textContent).toBe('00:00');
  });

  it('clamps to 00:00 for already-expired timestamps', () => {
    vi.setSystemTime(new Date('2026-05-19T12:01:00Z'));
    render(<CountdownTimer expiresAt="2026-05-19T12:00:00Z" />);
    expect(screen.getByTestId('countdown-value').textContent).toBe('00:00');
  });
});

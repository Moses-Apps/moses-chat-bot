// Settings page — toggle persists, DND quick-pick updates store, axe clean.

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { fireEvent, render, screen } from '@testing-library/react';
import { axe } from 'jest-axe';
import { useSettingsStore } from '@/stores/settingsStore';
import Settings from './Settings';

function reset(): void {
  useSettingsStore.setState({
    notifyDeployments: true,
    notifyTicketCompletion: true,
    notifyAutopilotSummaries: true,
    notifyErrors: true,
    dndUntil: null,
    dndSchedule: { start: '22:00', end: '08:00', enabled: false },
    autopilotMaxConcurrent: 3,
    autopilotTimeoutHours: 24,
  });
}

beforeEach(() => {
  reset();
  // Settings persistence writes to localStorage on every change; clear it.
  localStorage.removeItem('moses-chat-bot:settings');
});

afterEach(() => {
  vi.useRealTimers();
});

describe('<Settings />', () => {
  it('toggling "Receive deployment notifications" flips store + persists', () => {
    render(<Settings />);
    const sw = screen.getByRole('switch', { name: /deployment notifications/i });
    expect(sw).toHaveAttribute('aria-checked', 'true');
    fireEvent.click(sw);
    expect(useSettingsStore.getState().notifyDeployments).toBe(false);
    // Persisted to localStorage.
    const raw = localStorage.getItem('moses-chat-bot:settings');
    expect(raw).not.toBeNull();
    const parsed = JSON.parse(raw!);
    expect(parsed.state.notifyDeployments).toBe(false);
  });

  it('DND quick-pick "30 min" updates dndUntil in the store', () => {
    const now = new Date('2026-05-19T10:00:00Z').getTime();
    const spy = vi.spyOn(Date, 'now').mockReturnValue(now);
    try {
      render(<Settings />);
      fireEvent.click(screen.getByRole('button', { name: /30 min/i }));
      const dnd = useSettingsStore.getState().dndUntil;
      expect(dnd).not.toBeNull();
      expect(new Date(dnd!).getTime()).toBe(now + 30 * 60 * 1000);
      // The active-DND banner appears.
      expect(screen.getByText(/snoozed until/i)).toBeInTheDocument();
    } finally {
      spy.mockRestore();
    }
  });

  it('"Resume now" clears the DND', () => {
    useSettingsStore.setState({
      dndUntil: new Date(Date.now() + 60_000).toISOString(),
    });
    render(<Settings />);
    fireEvent.click(screen.getByRole('button', { name: /resume now/i }));
    expect(useSettingsStore.getState().dndUntil).toBeNull();
  });

  it('autopilot sliders update store values', () => {
    render(<Settings />);
    const slider = screen.getByLabelText(/max concurrent agents/i) as HTMLInputElement;
    fireEvent.change(slider, { target: { value: '7' } });
    expect(useSettingsStore.getState().autopilotMaxConcurrent).toBe(7);
  });

  it('has no axe violations', async () => {
    const { container } = render(<Settings />);
    const results = await axe(container);
    expect(results).toHaveNoViolations();
  });
});

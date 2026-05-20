// Zustand store for user-scoped per-device settings.
//
// v1 is localStorage-only via Zustand's `persist`. The UI auto-saves on every
// change; the Settings page surfaces a toast on every write.
//
// These settings are *advisory* — the bot doesn't yet gate outbound pushes on
// the notification toggles, and autopilotMaxConcurrent / autopilotTimeoutHours
// are just defaults the slash-command can pick up. A follow-up bead tracks
// server-side cross-device sync (see moses-chat-bot beads at the end of T-FE-3).

import { create } from 'zustand';
import { persist, createJSONStorage } from 'zustand/middleware';

export interface DndSchedule {
  /** Local-time HH:MM (24h). Inclusive start of the recurring quiet window. */
  start: string;
  /** Local-time HH:MM (24h). Exclusive end. Wraps midnight if end <= start. */
  end: string;
  enabled: boolean;
}

export interface SettingsState {
  // ── Notifications (advisory; the MM-skill respects these) ────────────────
  notifyDeployments: boolean;
  notifyTicketCompletion: boolean;
  notifyAutopilotSummaries: boolean;
  notifyErrors: boolean;

  // ── Do-not-disturb ───────────────────────────────────────────────────────
  /** ISO-8601 timestamp until which outbound pushes are snoozed; null = off. */
  dndUntil: string | null;
  /** Daily recurring DND window in the user's local timezone. */
  dndSchedule: DndSchedule;

  // ── Autopilot defaults (echoed by /autopilot start) ─────────────────────
  autopilotMaxConcurrent: number;
  autopilotTimeoutHours: number;

  // ── Actions ──────────────────────────────────────────────────────────────
  /**
   * Snooze for `durationMs` milliseconds from now, or clear when durationMs is 0.
   */
  setDnd: (durationMs: number) => void;
  /** Convenience: clears dndUntil. */
  clearDnd: () => void;
  /** Replace one or more notification toggles. */
  setNotificationPref: (
    next: Partial<Pick<SettingsState,
      'notifyDeployments'
      | 'notifyTicketCompletion'
      | 'notifyAutopilotSummaries'
      | 'notifyErrors'
    >>,
  ) => void;
  setDndSchedule: (next: Partial<DndSchedule>) => void;
  setAutopilotDefaults: (
    next: Partial<Pick<SettingsState,
      'autopilotMaxConcurrent' | 'autopilotTimeoutHours'
    >>,
  ) => void;
}

const DEFAULTS = {
  notifyDeployments: true,
  notifyTicketCompletion: true,
  notifyAutopilotSummaries: true,
  notifyErrors: true,
  dndUntil: null as string | null,
  dndSchedule: { start: '22:00', end: '08:00', enabled: false } as DndSchedule,
  autopilotMaxConcurrent: 3,
  autopilotTimeoutHours: 24,
};

export const useSettingsStore = create<SettingsState>()(
  persist(
    (set, get) => ({
      ...DEFAULTS,
      setDnd: (durationMs) => {
        if (!durationMs || durationMs <= 0) {
          set({ dndUntil: null });
          return;
        }
        set({ dndUntil: new Date(Date.now() + durationMs).toISOString() });
      },
      clearDnd: () => set({ dndUntil: null }),
      setNotificationPref: (next) => set({ ...get(), ...next }),
      setDndSchedule: (next) =>
        set({ dndSchedule: { ...get().dndSchedule, ...next } }),
      setAutopilotDefaults: (next) => set({ ...get(), ...next }),
    }),
    {
      name: 'moses-chat-bot:settings',
      storage: createJSONStorage(() => localStorage),
      version: 2,
      migrate: (persisted, version) => {
        // v1 → v2 added notification toggles, dndSchedule, autopilot defaults.
        if (version < 2) {
          const partial = (persisted as Partial<SettingsState>) ?? {};
          return { ...DEFAULTS, ...partial } as SettingsState;
        }
        return persisted as SettingsState;
      },
    },
  ),
);

// Global Settings page (per-user, persisted to localStorage via Zustand).
//
// v1 is localStorage-only — these are UI-side preferences (which categories
// the user wants to *see*, what hours to mute pushes, what defaults the
// /autopilot slash command should suggest). The MM-skill (bot-architecture)
// is the thing that ultimately respects them at push time. A follow-up bead
// tracks server-side cross-device sync.
//
// Auto-save: every control change writes to the store, then a "Saved" toast
// surfaces top-right.

import { useEffect, useState, type ReactElement } from 'react';
import BentoCard from '@/components/layout/BentoCard';
import Toggle from '@/components/settings/Toggle';
import Slider from '@/components/settings/Slider';
import DurationSelector from '@/components/settings/DurationSelector';
import { ToastProvider, useToast } from '@/components/Toast';
import { useSettingsStore } from '@/stores/settingsStore';

const absoluteFormatter = new Intl.DateTimeFormat(undefined, {
  dateStyle: 'medium',
  timeStyle: 'short',
});

function SettingsBody(): ReactElement {
  const { show } = useToast();
  const settings = useSettingsStore();

  // Surface a single "Saved" toast on every store write.
  // We compare a stringified snapshot (cheap; the store is shallow + bounded).
  const [lastSnapshot, setLastSnapshot] = useState<string | null>(null);
  useEffect(() => {
    const snapshot = JSON.stringify({
      n1: settings.notifyDeployments,
      n2: settings.notifyTicketCompletion,
      n3: settings.notifyAutopilotSummaries,
      n4: settings.notifyErrors,
      dnd: settings.dndUntil,
      sched: settings.dndSchedule,
      apMax: settings.autopilotMaxConcurrent,
      apTo: settings.autopilotTimeoutHours,
    });
    if (lastSnapshot !== null && lastSnapshot !== snapshot) {
      show('Saved');
    }
    setLastSnapshot(snapshot);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [
    settings.notifyDeployments,
    settings.notifyTicketCompletion,
    settings.notifyAutopilotSummaries,
    settings.notifyErrors,
    settings.dndUntil,
    settings.dndSchedule.start,
    settings.dndSchedule.end,
    settings.dndSchedule.enabled,
    settings.autopilotMaxConcurrent,
    settings.autopilotTimeoutHours,
  ]);

  const dndActive =
    settings.dndUntil !== null && new Date(settings.dndUntil).getTime() > Date.now();

  return (
    <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
      <BentoCard
        title="Notifications"
        subtitle="Which push categories the bot should send"
      >
        <p className="mb-2 text-xs text-moses-text-muted">
          {/* TODO: gate outbound on these toggles once the MM-skill respects them. */}
          Advisory for v1. The Moses Manager skill consults these before fanning out a push.
        </p>
        <div className="divide-y divide-moses-border dark:divide-moses-border-dark">
          <Toggle
            checked={settings.notifyDeployments}
            onChange={(v) => settings.setNotificationPref({ notifyDeployments: v })}
            label="Deployment notifications"
            description="Build succeeded / failed, rollouts, image promotions."
          />
          <Toggle
            checked={settings.notifyTicketCompletion}
            onChange={(v) =>
              settings.setNotificationPref({ notifyTicketCompletion: v })
            }
            label="Ticket completion notifications"
            description="Tickets reaching DONE, REVIEW, or BLOCKED."
          />
          <Toggle
            checked={settings.notifyAutopilotSummaries}
            onChange={(v) =>
              settings.setNotificationPref({ notifyAutopilotSummaries: v })
            }
            label="Autopilot summaries"
            description="Periodic rollups while an autopilot session is active."
          />
          <Toggle
            checked={settings.notifyErrors}
            onChange={(v) => settings.setNotificationPref({ notifyErrors: v })}
            label="Show error notifications"
            description="Agent failures, deploy regressions, trigger-engine alerts."
          />
        </div>
      </BentoCard>

      <BentoCard
        title="Do not disturb"
        subtitle="Mute pushes during a window or for the next few hours"
      >
        {dndActive && settings.dndUntil && (
          <div
            role="status"
            className="mb-4 flex flex-col gap-2 rounded-bento border border-moses-status-pending/40 bg-moses-status-pending/10 p-3 text-sm sm:flex-row sm:items-center sm:justify-between"
          >
            <p className="text-moses-status-pending">
              Snoozed until {absoluteFormatter.format(new Date(settings.dndUntil))}
            </p>
            <button
              type="button"
              onClick={() => settings.clearDnd()}
              className="min-h-[44px] rounded-bento border border-moses-status-pending/50 px-3 text-sm font-medium text-moses-status-pending hover:bg-moses-status-pending/10 focus:outline-none focus:ring-2 focus:ring-moses-status-pending/40"
            >
              Resume now
            </button>
          </div>
        )}

        <div className="space-y-4">
          <div>
            <p className="text-sm font-medium text-moses-text">Snooze now</p>
            <p className="mt-1 text-xs text-moses-text-muted">
              Pick a quick duration to mute pushes from this moment.
            </p>
            <div className="mt-2">
              <DurationSelector onSelect={(ms) => settings.setDnd(ms)} />
            </div>
          </div>

          <fieldset className="space-y-3 border-t border-moses-border pt-4 dark:border-moses-border-dark">
            <legend className="text-sm font-medium text-moses-text">
              Recurring quiet hours
            </legend>
            <Toggle
              checked={settings.dndSchedule.enabled}
              onChange={(v) => settings.setDndSchedule({ enabled: v })}
              label="Enable daily quiet hours"
              description="Pushes pause automatically every day during this window."
            />
            <div className="flex flex-wrap items-center gap-3">
              <label className="flex items-center gap-2 text-xs text-moses-text-muted">
                Start
                <input
                  type="time"
                  aria-label="Quiet hours start"
                  value={settings.dndSchedule.start}
                  onChange={(e) => settings.setDndSchedule({ start: e.target.value })}
                  className="min-h-[44px] rounded-bento border border-moses-border bg-moses-surface-raised px-2 text-sm text-moses-text focus:border-moses-accent focus:outline-none focus:ring-2 focus:ring-moses-accent/30 dark:border-moses-border-dark dark:bg-moses-surface-dark-raised dark:text-moses-text-inverse"
                />
              </label>
              <label className="flex items-center gap-2 text-xs text-moses-text-muted">
                End
                <input
                  type="time"
                  aria-label="Quiet hours end"
                  value={settings.dndSchedule.end}
                  onChange={(e) => settings.setDndSchedule({ end: e.target.value })}
                  className="min-h-[44px] rounded-bento border border-moses-border bg-moses-surface-raised px-2 text-sm text-moses-text focus:border-moses-accent focus:outline-none focus:ring-2 focus:ring-moses-accent/30 dark:border-moses-border-dark dark:bg-moses-surface-dark-raised dark:text-moses-text-inverse"
                />
              </label>
            </div>
          </fieldset>
        </div>
      </BentoCard>

      <BentoCard
        title="Autopilot defaults"
        subtitle="Pre-filled when you say /autopilot start from Telegram"
        className="lg:col-span-2"
      >
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
          <Slider
            value={settings.autopilotMaxConcurrent}
            onChange={(v) => settings.setAutopilotDefaults({ autopilotMaxConcurrent: v })}
            min={1}
            max={10}
            label="Max concurrent agents"
            unit=" agents"
            description="Cap on parallel agent pods inside a session."
          />
          <Slider
            value={settings.autopilotTimeoutHours}
            onChange={(v) => settings.setAutopilotDefaults({ autopilotTimeoutHours: v })}
            min={1}
            max={72}
            label="Session timeout"
            unit=" h"
            description="Autopilot stops automatically after this window."
          />
        </div>
        <p className="mt-2 text-xs text-moses-text-muted">
          These defaults apply when you say <code>/autopilot start</code> from
          Telegram.
        </p>
      </BentoCard>
    </div>
  );
}

export default function Settings(): ReactElement {
  return (
    <ToastProvider>
      <SettingsBody />
    </ToastProvider>
  );
}

// LinkDetail — Activity / Settings / Danger tabs for one relay link.
//
// On mount we fetch links (if the store is empty) so that `selectLink`
// resolves the row even on a deep refresh, then load the last 100 messages.

import { useEffect, useState, type ReactElement } from 'react';
import { useNavigate, useParams } from 'react-router-dom';

import BentoCard from '@/components/layout/BentoCard';
import StatusBadge from '@/components/StatusBadge';
import ConfirmDialog from '@/components/ConfirmDialog';
import Tabs from '@/components/Tabs';
import MessageList from '@/components/links/MessageList';
import ProviderIcon from '@/components/links/ProviderIcon';
import { useLinksStore } from '@/stores/linksStore';
import { useMessagesStore } from '@/stores/messagesStore';

const absoluteFormatter = new Intl.DateTimeFormat(undefined, {
  dateStyle: 'medium',
  timeStyle: 'short',
});

function formatTime(iso: string | null | undefined): string {
  if (!iso) return 'Never used yet';
  const t = new Date(iso).getTime();
  return Number.isNaN(t) ? 'Unknown' : absoluteFormatter.format(t);
}

function providerLabel(provider: string): string {
  if (provider === 'telegram') return 'Telegram';
  return provider.charAt(0).toUpperCase() + provider.slice(1);
}

export default function LinkDetail(): ReactElement {
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const links = useLinksStore((s) => s.links);
  const currentLink = useLinksStore((s) => s.currentLink);
  const fetchLinks = useLinksStore((s) => s.fetchLinks);
  const selectLink = useLinksStore((s) => s.selectLink);
  const unlink = useLinksStore((s) => s.unlink);

  const messages = useMessagesStore((s) => s.messages);
  const messagesLoading = useMessagesStore((s) => s.loading);
  const messagesError = useMessagesStore((s) => s.error);
  const fetchMessages = useMessagesStore((s) => s.fetchMessages);

  const [tab, setTab] = useState<string>('activity');
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [unlinkBusy, setUnlinkBusy] = useState(false);
  const [unlinkError, setUnlinkError] = useState<string | null>(null);

  // Load links list if empty (deep-refresh path), then resolve currentLink.
  useEffect(() => {
    if (links.length === 0) void fetchLinks();
  }, [links.length, fetchLinks]);

  useEffect(() => {
    if (!id) return;
    selectLink(id);
    void fetchMessages(id, 100);
  }, [id, links.length, selectLink, fetchMessages]);

  async function onConfirmUnlink(): Promise<void> {
    if (!id) return;
    setUnlinkBusy(true);
    setUnlinkError(null);
    try {
      await unlink(id);
      setConfirmOpen(false);
      navigate('/');
    } catch (err) {
      const msg = err instanceof Error ? err.message : 'Could not unlink.';
      setUnlinkError(msg);
    } finally {
      setUnlinkBusy(false);
    }
  }

  if (!id) {
    return (
      <BentoCard title="Link not found">
        <p className="text-sm text-moses-text-muted">No link id in the URL.</p>
      </BentoCard>
    );
  }

  return (
    <div className="space-y-4">
      <BentoCard
        title={currentLink ? providerLabel(currentLink.provider) : 'Loading link…'}
        subtitle={
          currentLink
            ? currentLink.providerDisplayName ?? currentLink.providerUserId
            : undefined
        }
        trailing={
          currentLink ? (
            <StatusBadge status={currentLink.isActive ? 'active' : 'inactive'} />
          ) : null
        }
      >
        {currentLink ? (
          <div className="flex items-center gap-3 text-sm text-moses-text-muted">
            <span className="flex h-9 w-9 items-center justify-center rounded-full bg-moses-accent-soft text-moses-accent">
              <ProviderIcon provider={currentLink.provider} className="h-4 w-4" />
            </span>
            <span>Last used: {formatTime(currentLink.lastUsedAt)}</span>
          </div>
        ) : (
          <p className="text-sm text-moses-text-muted" aria-busy="true">
            Loading link details…
          </p>
        )}
      </BentoCard>

      <BentoCard>
        <Tabs
          ariaLabel="Link sections"
          value={tab}
          onChange={setTab}
          items={[
            {
              id: 'activity',
              label: 'Activity',
              content: messagesLoading && messages.length === 0 ? (
                <p className="text-sm text-moses-text-muted" aria-busy="true">
                  Loading messages…
                </p>
              ) : messagesError ? (
                <p role="alert" className="text-sm text-moses-status-error">
                  Could not load messages: {messagesError.message}
                </p>
              ) : (
                <MessageList messages={messages} />
              ),
            },
            {
              id: 'settings',
              label: 'Settings',
              content: (
                <p className="rounded-bento border border-dashed border-moses-border p-6 text-center text-sm text-moses-text-muted dark:border-moses-border-dark">
                  Settings UI lands in T-FE-3.
                </p>
              ),
            },
            {
              id: 'danger',
              label: 'Danger',
              tone: 'danger',
              content: (
                <div className="rounded-bento border border-moses-status-error/40 bg-moses-status-error/5 p-4">
                  <h3 className="text-sm font-semibold text-moses-status-error">
                    Unlink this chat
                  </h3>
                  <p className="mt-1 text-sm text-moses-text-muted">
                    The chat will stop relaying messages, and the underlying
                    Moses API key will be revoked. You can re-link any time.
                  </p>
                  {unlinkError && (
                    <p role="alert" className="mt-2 text-sm text-moses-status-error">
                      {unlinkError}
                    </p>
                  )}
                  <button
                    type="button"
                    onClick={() => setConfirmOpen(true)}
                    className="mt-4 min-h-[44px] rounded-bento bg-moses-status-error px-4 text-sm font-semibold text-white hover:bg-moses-status-error/90 focus:outline-none focus:ring-2 focus:ring-moses-status-error/40"
                  >
                    Unlink
                  </button>
                </div>
              ),
            },
          ]}
        />
      </BentoCard>

      <ConfirmDialog
        open={confirmOpen}
        title="Unlink this chat?"
        description="You'll need to re-link to use it again. The API key will be revoked from Moses."
        confirmLabel="Unlink"
        destructive
        busy={unlinkBusy}
        onConfirm={() => void onConfirmUnlink()}
        onCancel={() => {
          if (!unlinkBusy) {
            setConfirmOpen(false);
            setUnlinkError(null);
          }
        }}
      />
    </div>
  );
}

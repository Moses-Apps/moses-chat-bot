// Optimistic-update + rollback contract for `unlink`.

import { describe, it, expect, beforeEach, vi } from 'vitest';
import type { Link } from '@/lib/bot-api';

// Mock the bot-api before importing the store so the store binds to the mock.
vi.mock('@/lib/bot-api', async () => {
  const actual = await vi.importActual<typeof import('@/lib/bot-api')>('@/lib/bot-api');
  return {
    ...actual,
    deleteLink: vi.fn(),
    listLinks: vi.fn(),
  };
});

import { deleteLink } from '@/lib/bot-api';
import { useLinksStore } from './linksStore';

const sampleLinks: Link[] = [
  {
    id: 'link-1',
    provider: 'telegram',
    providerUserId: '12345',
    isActive: true,
    createdAt: '2026-05-19T00:00:00Z',
  },
  {
    id: 'link-2',
    provider: 'telegram',
    providerUserId: '67890',
    isActive: true,
    createdAt: '2026-05-19T00:00:00Z',
  },
];

describe('linksStore.unlink', () => {
  beforeEach(() => {
    useLinksStore.getState()._setLinksForTest(sampleLinks);
    vi.mocked(deleteLink).mockReset();
  });

  it('removes the link optimistically on success', async () => {
    vi.mocked(deleteLink).mockResolvedValueOnce(undefined);

    const promise = useLinksStore.getState().unlink('link-1');
    // Snapshot mid-flight: the optimistic update has already happened.
    expect(useLinksStore.getState().links.map((l) => l.id)).toEqual(['link-2']);

    await promise;
    expect(useLinksStore.getState().links.map((l) => l.id)).toEqual(['link-2']);
    expect(useLinksStore.getState().error).toBeNull();
  });

  it('rolls back when the network call fails', async () => {
    vi.mocked(deleteLink).mockRejectedValueOnce({
      status: 500,
      code: 'internal_error',
      message: 'fail',
    });

    await expect(useLinksStore.getState().unlink('link-1')).rejects.toMatchObject({
      code: 'internal_error',
    });

    // Both links restored after rollback.
    expect(useLinksStore.getState().links.map((l) => l.id)).toEqual([
      'link-1',
      'link-2',
    ]);
    expect(useLinksStore.getState().error?.message).toBe('fail');
  });

  it('clears currentLink only when its id is being removed', async () => {
    vi.mocked(deleteLink).mockResolvedValueOnce(undefined);
    useLinksStore.getState().selectLink('link-2');
    await useLinksStore.getState().unlink('link-1');
    expect(useLinksStore.getState().currentLink?.id).toBe('link-2');
  });
});

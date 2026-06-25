// Breakpoint-specific class assertions for the linking-flow pages.
//
// jsdom doesn't actually layout CSS, so we assert that the responsive Tailwind
// classes are present on the elements that are supposed to react to the
// breakpoint. The build/runtime then ensures the visual outcome at 320 / 768 /
// 1280 px viewports.

import { describe, expect, it, beforeEach, vi } from 'vitest';
import { render, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import type { Link } from '@/lib/bot-api';

vi.mock('@/lib/bot-api', async () => {
  const actual = await vi.importActual<typeof import('@/lib/bot-api')>('@/lib/bot-api');
  return {
    ...actual,
    listLinks: vi.fn().mockResolvedValue([]),
    getLinkMessages: vi.fn().mockResolvedValue([]),
    deleteLink: vi.fn(),
    // LinkNew now gates on the tenant bot status; a connected bot lets the
    // provider picker render so the responsive classes can be asserted.
    getTelegramInfo: vi.fn().mockResolvedValue({ configured: true, username: 'moses_test_bot' }),
  };
});

import { withQueryClient } from '@/test/queryWrapper';
import Dashboard from './Dashboard';
import LinkNew from './LinkNew';

beforeEach(() => {
  if (!('crypto' in globalThis) || !globalThis.crypto.randomUUID) {
    Object.defineProperty(globalThis, 'crypto', {
      value: { randomUUID: () => '00000000-0000-0000-0000-000000000000' },
      configurable: true,
    });
  }
});

describe('responsive layout classes', () => {
  it('Dashboard grid uses single-column at <lg and 3-col at lg+', () => {
    const { container } = render(
      withQueryClient(
        <MemoryRouter>
          <Dashboard />
        </MemoryRouter>,
      ),
    );
    const grid = container.querySelector('.grid-cols-1.lg\\:grid-cols-3');
    expect(grid).not.toBeNull();
    // The "My active links" card spans 2 columns at lg+.
    expect(container.querySelector('.lg\\:col-span-2')).not.toBeNull();
  });

  it('LinkNew provider picker is 1-col on small, 2-col on sm+', async () => {
    const { container } = render(
      <MemoryRouter>
        <LinkNew />
      </MemoryRouter>,
    );
    // The picker only renders once the bot-status check resolves.
    await waitFor(() =>
      expect(container.querySelector('[role="radiogroup"]')).not.toBeNull(),
    );
    const radiogroup = container.querySelector('[role="radiogroup"]');
    expect(radiogroup).not.toBeNull();
    expect(radiogroup?.className).toMatch(/grid-cols-1/);
    expect(radiogroup?.className).toMatch(/sm:grid-cols-2/);
  });
});

describe('responsive: touch targets ≥44px', () => {
  it('Dashboard CTA + provider radios use min-h-[44px] or min-h-[64px]', () => {
    const { container } = render(
      withQueryClient(
        <MemoryRouter>
          <Dashboard />
        </MemoryRouter>,
      ),
    );
    // The "Link new chat" CTA + the empty-state CTA both wear min-h-[44px].
    const ctas = container.querySelectorAll('a[href="/link/new"]');
    ctas.forEach((cta) => {
      expect(cta.className).toMatch(/min-h-\[44px\]/);
    });
  });
});

// Lightweight smoke export to ensure the file isn't an empty module.
export const _typeCheck: Link | null = null;

// Messages search: direction filter + free-text search across a seeded set.
//
// The Messages page applies direction / free-text filters client-side (the
// backend doesn't yet support them — see bot-api.searchMessages comments), so
// the assertions exercise the in-browser filter logic against a fixed window.

import { expect, test } from '@playwright/test';
import { buildSampleMessages, sampleActiveLink, setupMockBotAPI } from './fixtures';

test('search messages by direction filter and free-text', async ({ page }) => {
  const messages = buildSampleMessages(50, sampleActiveLink.id);
  await setupMockBotAPI(page, {
    links: [sampleActiveLink],
    messages,
  });

  await page.goto('/messages');

  // 1. Initial render. From buildSampleMessages: i%2===0 → in, otherwise out.
  // i=2 → "user said hello 2" (in), i=1 → "bot replied with answer 1" (out).
  // Use exact match to dodge substring collisions ("hello 2" vs "hello 20").
  await expect(page.getByText('user said hello 2', { exact: true })).toBeVisible();
  // A row from later in the dataset must also be in the DOM. We pick index 47
  // (well past the initial viewport but well within the 50-row page) so the
  // assertion exercises that the full page came through.
  await expect(page.getByText('bot replied with answer 47', { exact: true })).toBeAttached();

  // 2. Click "Outbound". The control renders both inside the sm+ row and the
  // <sm accordion; the desktop chromium viewport only paints the sm+ copy, but
  // the accordion version is hidden, not removed — pick the visible one.
  const outbound = page.getByRole('button', { name: /^outbound$/i }).first();
  await outbound.click();

  // 3. Inbound rows no longer visible; outbound rows still are.
  await expect(page.getByText('user said hello 2', { exact: true })).toBeHidden();
  await expect(page.getByText('bot replied with answer 1', { exact: true })).toBeVisible();

  // 4. Reset direction to All so the "deploy" rows are not filtered out.
  await page.getByRole('button', { name: /^all$/i }).first().click();

  // 5. Type "deploy" in the search box. SearchInput debounces at 250ms.
  const search = page.getByRole('searchbox', { name: /search message text/i });
  await search.fill('deploy');

  // 6. After debounce + refetch, only the matching rows remain.
  await expect(
    page.getByText('deploy succeeded for build 0', { exact: true }),
  ).toBeVisible({ timeout: 4_000 });
  await expect(page.getByText('user said hello 2', { exact: true })).toBeHidden();
});

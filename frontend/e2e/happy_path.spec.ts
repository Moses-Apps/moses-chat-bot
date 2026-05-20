// Happy path: dashboard → provider picker → generate code → success.
//
// Goals:
//   1. Empty dashboard renders.
//   2. CTA navigates to /link/new.
//   3. Provider picker shows; only Telegram enabled.
//   4. Generate code → 6-digit code visible + countdown.
//   5. Plaintext key never appears in the DOM (SPEC.md §4 step 4).
//   6. Polling completes → success screen → auto-redirect to /links/:id.

import { expect, test } from '@playwright/test';
import { setupMockBotAPI } from './fixtures';

test('happy path: link new Telegram chat', async ({ page }) => {
  const mock = await setupMockBotAPI(page, {
    links: [],
    pollPendingCount: 1, // first poll pending, next completes
    completedLinkId: 'link-happy-path-001',
  });

  // 1. Empty dashboard.
  await page.goto('/');
  await expect(
    page.getByRole('heading', { name: /connect your first chat/i }),
  ).toBeVisible();

  // 2. CTA → /link/new. Two CTAs exist (empty-state + side-card); both are
  // valid entry points. We click the empty-state one which says "Link a chat".
  await page.getByRole('link', { name: /^link a chat$/i }).click();
  await expect(page).toHaveURL(/\/link\/new$/);

  // 3. Provider picker.
  await expect(page.getByRole('radiogroup', { name: /choose a chat provider/i })).toBeVisible();
  const telegram = page.getByRole('radio', { name: /telegram/i });
  await expect(telegram).toBeEnabled();
  await expect(telegram).toBeChecked();
  const discord = page.getByRole('radio', { name: /discord/i });
  await expect(discord).toBeDisabled();

  // 4. Generate code.
  await page.getByRole('button', { name: /generate code/i }).click();

  // 5. Big code displayed + countdown timer.
  await expect(page.getByRole('heading', { name: /enter this code in telegram/i })).toBeVisible();
  // The screen-reader-friendly aria-label exposes the full numeric code.
  await expect(page.getByLabel(/^linking code a 1 b 2 c 3$/i)).toBeVisible();
  // Countdown tile shows mm:ss in font-mono.
  const countdown = page.getByTestId('countdown-value');
  await expect(countdown).toBeVisible();
  await expect(countdown).toHaveText(/^\d{2}:\d{2}$/);

  // 6. Plaintext key must NOT be visible anywhere in the rendered page.
  const dom = await page.content();
  expect(dom).not.toContain('mcp-e2e-deadbeef-never-leaks-to-dom');

  // 7. Wait for polling to flip to completed (mock returns completed on poll
  // #2 — interval is 2s, so within ~6s the success screen lands).
  await expect(page.getByText(/linked successfully/i)).toBeVisible({ timeout: 10_000 });

  // 8. Auto-redirect to /links/:id (LinkNew uses a 2s setTimeout).
  await expect(page).toHaveURL(/\/links\/link-happy-path-001$/, { timeout: 6_000 });

  // 9. The mint endpoint was hit exactly once.
  expect(mock.mintedCodes).toEqual(['a1b2c3']);
});

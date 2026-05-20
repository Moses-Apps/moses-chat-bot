// Unlink flow: dashboard with one link → detail → Danger tab → confirm unlink
// → assert DELETE was called → redirect to dashboard → empty state visible.

import { expect, test } from '@playwright/test';
import { sampleActiveLink, setupMockBotAPI } from './fixtures';

test('unlink existing link', async ({ page }) => {
  const mock = await setupMockBotAPI(page, { links: [sampleActiveLink] });

  // 1. Dashboard shows the seeded link.
  await page.goto('/');
  await expect(page.getByText(/e2e tester/i)).toBeVisible();

  // 2. Open the link card (LinkCard renders an internal RouterLink).
  await page.getByRole('link', { name: /e2e tester/i }).first().click();
  await expect(page).toHaveURL(/\/links\/link-active-001$/);

  // 3. Switch to the Danger tab.
  await page.getByRole('tab', { name: /danger/i }).click();
  const unlinkBtn = page.getByRole('button', { name: /^unlink$/i });
  await expect(unlinkBtn).toBeVisible();

  // 4. Open the confirm dialog.
  await unlinkBtn.click();
  const dialog = page.getByRole('dialog');
  await expect(dialog).toBeVisible();
  await expect(dialog).toHaveAttribute('aria-modal', 'true');
  await expect(dialog).toContainText(/unlink this chat\?/i);

  // 5. Confirm. The button inside the dialog is wired with data-dialog-confirm.
  await dialog.locator('[data-dialog-confirm]').click();

  // 6. Assert the DELETE landed at the mock.
  await expect.poll(() => mock.deletedLinks).toContain('link-active-001');

  // 7. Auto-redirect back to dashboard, and the empty state surfaces.
  await expect(page).toHaveURL(/\/$/);
  await expect(
    page.getByRole('heading', { name: /connect your first chat/i }),
  ).toBeVisible();
});

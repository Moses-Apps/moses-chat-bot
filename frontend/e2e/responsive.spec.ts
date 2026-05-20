// Responsive: small phone viewport doesn't cause horizontal overflow + the
// provider picker stacks vertically (single column) at <sm.
//
// We pick 320×568 (iPhone SE-class) as the floor — SPEC.md §10 calls out this
// as the supported lower bound for the Tauri-embedded webview.

import { expect, test } from '@playwright/test';
import { setupMockBotAPI } from './fixtures';

test.describe('mobile viewport (320×568)', () => {
  test.use({ viewport: { width: 320, height: 568 } });

  test('dashboard renders without horizontal overflow + nav reaches link picker', async ({ page }) => {
    await setupMockBotAPI(page);

    await page.goto('/');
    // Dashboard root grid is the single-column variant; nothing should be
    // wider than the viewport.
    const docWidth = await page.evaluate(() => document.documentElement.scrollWidth);
    expect(docWidth).toBeLessThanOrEqual(320);

    // Navigate to /link/new. The "Link a chat" CTA is the empty-state button.
    await page.getByRole('link', { name: /^link a chat$/i }).click();
    await expect(page).toHaveURL(/\/link\/new$/);

    // Provider picker visible. At sm- (320px), the radiogroup uses grid-cols-1
    // (no sm:grid-cols-2 active). We assert the grid wraps to a single column
    // by checking the bounding boxes of two siblings: their top-coords differ.
    const radios = page.getByRole('radio');
    const telegramBox = await radios.nth(0).boundingBox();
    const discordBox = await radios.nth(1).boundingBox();
    expect(telegramBox).not.toBeNull();
    expect(discordBox).not.toBeNull();
    if (telegramBox && discordBox) {
      // Stacked vertically → discord starts below telegram.
      expect(discordBox.y).toBeGreaterThan(telegramBox.y);
    }
  });
});

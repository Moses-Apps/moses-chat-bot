// Settings persistence: toggle a switch, see the Saved toast, reload, and
// confirm Zustand's persist middleware restored the value from localStorage.

import { expect, test } from '@playwright/test';
import { setupMockBotAPI } from './fixtures';

test('toggle notification preference persists across reload', async ({ page }) => {
  await setupMockBotAPI(page);

  await page.goto('/settings');
  const toggle = page.getByRole('switch', { name: /autopilot summaries/i });
  await expect(toggle).toBeVisible();
  await expect(toggle).toHaveAttribute('aria-checked', 'true');

  // Flip off; the toggle re-renders + a "Saved" toast appears top-right.
  await toggle.click();
  await expect(toggle).toHaveAttribute('aria-checked', 'false');
  await expect(page.getByTestId('toast')).toHaveText(/saved/i);

  // Reload — Zustand should hydrate from localStorage.
  await page.reload();
  const reloaded = page.getByRole('switch', { name: /autopilot summaries/i });
  await expect(reloaded).toHaveAttribute('aria-checked', 'false');
});

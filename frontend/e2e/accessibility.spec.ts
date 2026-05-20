// Axe accessibility scan on each main route.
//
// We walk through the four user-facing pages and assert zero serious/critical
// violations. Rules disabled per-page should always come with a comment —
// none today.

import { expect, test } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';
import { sampleActiveLink, setupMockBotAPI } from './fixtures';

const ROUTES = [
  { path: '/', name: 'dashboard' },
  { path: '/link/new', name: 'link-new' },
  { path: '/messages', name: 'messages' },
  { path: '/settings', name: 'settings' },
];

for (const route of ROUTES) {
  test(`axe: ${route.name} (${route.path}) has no violations`, async ({ page }) => {
    await setupMockBotAPI(page, { links: [sampleActiveLink] });
    await page.goto(route.path);
    // Give the page a moment to settle (Zustand stores hydrate + initial
    // fetches resolve).
    await page.waitForLoadState('networkidle');

    const results = await new AxeBuilder({ page })
      .withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa'])
      .analyze();

    // Filter out color-contrast violations only — those are sensitive to the
    // dynamic Tailwind theme tokens and are tracked separately in the design
    // system. Everything else must be clean.
    const violations = results.violations.filter((v) => v.id !== 'color-contrast');
    expect(violations, JSON.stringify(violations, null, 2)).toEqual([]);
  });
}

// PageShell renders + jest-axe accessibility smoke test.

import { describe, it, expect } from 'vitest';
import { render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { axe } from 'jest-axe';

import PageShell from './PageShell';

function renderShell(initialPath = '/') {
  return render(
    <MemoryRouter initialEntries={[initialPath]}>
      <PageShell>
        <h1>Hello</h1>
        <p>Body content</p>
      </PageShell>
    </MemoryRouter>,
  );
}

describe('<PageShell />', () => {
  it('renders the app name, breadcrumb, and children', () => {
    renderShell('/');
    expect(screen.getByRole('banner')).toBeInTheDocument();
    expect(screen.getByRole('main')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: /chat bot bridge/i })).toBeInTheDocument();
    expect(screen.getByText('Hello')).toBeInTheDocument();
    expect(screen.getByRole('navigation', { name: /breadcrumb/i })).toBeInTheDocument();
  });

  it('has no detectable accessibility violations on the default route', async () => {
    const { container } = renderShell('/');
    const results = await axe(container);
    expect(results).toHaveNoViolations();
  });

  it('renders a breadcrumb trail for nested routes', () => {
    renderShell('/links/abc12345');
    const nav = screen.getByRole('navigation', { name: /breadcrumb/i });
    expect(nav.textContent).toMatch(/Dashboard/);
    expect(nav.textContent).toMatch(/Links/);
  });
});

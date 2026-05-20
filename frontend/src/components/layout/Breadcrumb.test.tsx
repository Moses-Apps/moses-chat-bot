// Breadcrumb derivation from the current route.
//
// - Always starts with a "Dashboard" home crumb.
// - Friendly labels for known segments.
// - UUID-like segments collapse to "…<last 6>".
// - The final crumb has aria-current="page" and no link.

import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import Breadcrumb from './Breadcrumb';

function renderAt(path: string, routePattern = '*') {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path={routePattern} element={<Breadcrumb />} />
      </Routes>
    </MemoryRouter>,
  );
}

describe('<Breadcrumb />', () => {
  it('renders only "Dashboard" at the root', () => {
    renderAt('/');
    const nav = screen.getByLabelText('Breadcrumb');
    const items = nav.querySelectorAll('li');
    expect(items.length).toBe(1);
    expect(items[0]).toHaveTextContent(/dashboard/i);
    expect(items[0].querySelector('[aria-current="page"]')).not.toBeNull();
  });

  it('humanizes well-known segments', () => {
    renderAt('/messages');
    const nav = screen.getByLabelText('Breadcrumb');
    expect(nav).toHaveTextContent(/dashboard/i);
    expect(nav).toHaveTextContent(/messages/i);
    // The last crumb has aria-current="page".
    const currents = nav.querySelectorAll('[aria-current="page"]');
    expect(currents.length).toBe(1);
    expect(currents[0]).toHaveTextContent(/messages/i);
  });

  it('renders the linking-flow path: Dashboard / Link / New', () => {
    renderAt('/link/new');
    const nav = screen.getByLabelText('Breadcrumb');
    expect(nav).toHaveTextContent(/dashboard/i);
    expect(nav).toHaveTextContent(/link/i);
    expect(nav).toHaveTextContent(/new/i);
  });

  it('collapses opaque UUID-like segments to "…<last 6>"', () => {
    const id = '11111111-1111-1111-1111-111111111111';
    renderAt(`/links/${id}`, '/links/:id');
    const nav = screen.getByLabelText('Breadcrumb');
    // Expect the final crumb (currentpage) to show the collapsed form.
    const currents = nav.querySelectorAll('[aria-current="page"]');
    expect(currents.length).toBe(1);
    expect(currents[0].textContent).toMatch(/…111111/);
  });

  it('non-last crumbs are clickable links; the last is plain text', () => {
    renderAt('/link/new');
    const nav = screen.getByLabelText('Breadcrumb');
    const links = nav.querySelectorAll('a');
    // At minimum the "Dashboard" + "Link" segments are links.
    expect(links.length).toBeGreaterThanOrEqual(2);
    // The last <li> has no anchor inside — it's the current page.
    const items = nav.querySelectorAll('li');
    const lastItem = items[items.length - 1];
    expect(lastItem.querySelector('a')).toBeNull();
    expect(lastItem.querySelector('[aria-current="page"]')).not.toBeNull();
  });
});

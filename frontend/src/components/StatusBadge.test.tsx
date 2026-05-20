// StatusBadge — renders the four status kinds with appropriate color tokens
// and accessible labels.

import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import StatusBadge, { type StatusKind } from './StatusBadge';

describe('<StatusBadge />', () => {
  const cases: Array<{ status: StatusKind; defaultLabel: string; colorClass: string }> = [
    { status: 'active', defaultLabel: 'Active', colorClass: 'text-moses-status-active' },
    { status: 'inactive', defaultLabel: 'Inactive', colorClass: 'text-moses-status-inactive' },
    { status: 'pending', defaultLabel: 'Pending', colorClass: 'text-moses-status-pending' },
    { status: 'error', defaultLabel: 'Error', colorClass: 'text-moses-status-error' },
  ];

  it.each(cases)(
    'renders $status with default label "$defaultLabel" and the $colorClass token',
    ({ status, defaultLabel, colorClass }) => {
      const { container } = render(<StatusBadge status={status} />);
      const pill = container.querySelector('[role="status"]');
      expect(pill).not.toBeNull();
      expect(pill).toHaveTextContent(defaultLabel);
      expect(pill?.getAttribute('aria-label')).toBe(defaultLabel);
      expect(pill?.className).toContain(colorClass);
    },
  );

  it('honors a custom label and keeps the status-color tokens', () => {
    render(<StatusBadge status="active" label="Connected to Moses" />);
    const badge = screen.getByRole('status');
    expect(badge).toHaveTextContent('Connected to Moses');
    expect(badge.getAttribute('aria-label')).toBe('Connected to Moses');
    expect(badge.className).toContain('text-moses-status-active');
  });

  it('accepts and merges a custom className', () => {
    const { container } = render(
      <StatusBadge status="pending" className="ml-2 some-extra-class" />,
    );
    const badge = container.querySelector('[role="status"]');
    expect(badge?.className).toMatch(/some-extra-class/);
    expect(badge?.className).toMatch(/ml-2/);
  });
});

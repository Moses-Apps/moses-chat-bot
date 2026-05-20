// Toggle covers click + Space + Enter activation, disabled, and axe.

import { describe, it, expect, vi } from 'vitest';
import { fireEvent, render, screen } from '@testing-library/react';
import { axe } from 'jest-axe';
import Toggle from './Toggle';

describe('<Toggle />', () => {
  it('renders with role=switch and the correct aria-checked', () => {
    render(<Toggle checked={true} onChange={() => undefined} label="Notify me" />);
    const sw = screen.getByRole('switch', { name: /notify me/i });
    expect(sw).toHaveAttribute('aria-checked', 'true');
  });

  it('click flips state', () => {
    const onChange = vi.fn();
    render(<Toggle checked={false} onChange={onChange} label="Notify me" />);
    fireEvent.click(screen.getByRole('switch'));
    expect(onChange).toHaveBeenCalledWith(true);
  });

  it('keyboard activates via Space and Enter', () => {
    const onChange = vi.fn();
    render(<Toggle checked={false} onChange={onChange} label="Notify me" />);
    const sw = screen.getByRole('switch');
    sw.focus();
    fireEvent.keyDown(sw, { key: ' ' });
    fireEvent.keyDown(sw, { key: 'Enter' });
    expect(onChange).toHaveBeenCalledTimes(2);
  });

  it('disabled blocks change', () => {
    const onChange = vi.fn();
    render(
      <Toggle checked={false} onChange={onChange} label="Notify me" disabled />,
    );
    const sw = screen.getByRole('switch');
    fireEvent.click(sw);
    fireEvent.keyDown(sw, { key: ' ' });
    expect(onChange).not.toHaveBeenCalled();
  });

  it('has no axe violations', async () => {
    const { container } = render(
      <Toggle
        checked={true}
        onChange={() => undefined}
        label="Notify me"
        description="Helpful description"
      />,
    );
    const results = await axe(container);
    expect(results).toHaveNoViolations();
  });
});

// Toast appears + auto-dismisses + announces politely.

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { act, fireEvent, render, screen } from '@testing-library/react';
import { ToastProvider, useToast } from './Toast';
import type { ReactElement } from 'react';

function Probe(): ReactElement {
  const { show } = useToast();
  return (
    <button type="button" onClick={() => show('Saved')}>
      fire
    </button>
  );
}

describe('<Toast />', () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it('appears after show() and auto-dismisses after the duration', () => {
    render(
      <ToastProvider durationMs={1000}>
        <Probe />
      </ToastProvider>,
    );
    fireEvent.click(screen.getByText(/fire/i));
    expect(screen.getByTestId('toast')).toHaveTextContent('Saved');
    act(() => {
      vi.advanceTimersByTime(1001);
    });
    expect(screen.queryByTestId('toast')).toBeNull();
  });

  it('click dismisses immediately', () => {
    render(
      <ToastProvider durationMs={10000}>
        <Probe />
      </ToastProvider>,
    );
    fireEvent.click(screen.getByText(/fire/i));
    const toast = screen.getByTestId('toast');
    fireEvent.click(toast);
    expect(screen.queryByTestId('toast')).toBeNull();
  });

  it('announces politely via aria-live region', () => {
    render(
      <ToastProvider durationMs={1000}>
        <Probe />
      </ToastProvider>,
    );
    fireEvent.click(screen.getByText(/fire/i));
    const region = screen.getByTestId('toast').parentElement;
    expect(region).toHaveAttribute('aria-live', 'polite');
  });
});

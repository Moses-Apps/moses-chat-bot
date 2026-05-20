// ConfirmDialog — focus trap (Tab/Shift+Tab cycle), Escape cancels, backdrop
// click cancels, initial focus lands on Cancel (destructive flows are safe).

import { describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen } from '@testing-library/react';
import ConfirmDialog from './ConfirmDialog';

function renderDialog(overrides: Partial<Parameters<typeof ConfirmDialog>[0]> = {}) {
  const onConfirm = vi.fn();
  const onCancel = vi.fn();
  const utils = render(
    <ConfirmDialog
      open
      title="Test title"
      description="Some description"
      destructive
      onConfirm={onConfirm}
      onCancel={onCancel}
      {...overrides}
    />,
  );
  return { ...utils, onConfirm, onCancel };
}

describe('<ConfirmDialog />', () => {
  it('renders nothing when not open', () => {
    const { container } = render(
      <ConfirmDialog
        open={false}
        title="x"
        description="y"
        onConfirm={() => undefined}
        onCancel={() => undefined}
      />,
    );
    expect(container.firstChild).toBeNull();
  });

  it('places initial focus on the cancel button to keep destructive flows safe', async () => {
    renderDialog();
    // useEffect with setTimeout(0) — wait a tick.
    await new Promise((r) => setTimeout(r, 5));
    const cancel = screen.getByRole('button', { name: /cancel/i });
    expect(document.activeElement).toBe(cancel);
  });

  it('Escape calls onCancel', () => {
    const { onCancel } = renderDialog();
    fireEvent.keyDown(screen.getByRole('dialog'), { key: 'Escape' });
    expect(onCancel).toHaveBeenCalledTimes(1);
  });

  it('backdrop click calls onCancel; click inside the dialog does not', () => {
    const { onCancel } = renderDialog();
    const backdrop = screen.getByTestId('confirm-dialog-backdrop');
    // Click the dialog body — onCancel must NOT fire.
    fireEvent.click(screen.getByRole('dialog'));
    expect(onCancel).not.toHaveBeenCalled();
    // Click the backdrop itself.
    fireEvent.click(backdrop);
    expect(onCancel).toHaveBeenCalledTimes(1);
  });

  it('Tab from the last focusable wraps back to the first (focus trap)', async () => {
    renderDialog();
    await new Promise((r) => setTimeout(r, 5));
    const cancel = screen.getByRole('button', { name: /cancel/i });
    const confirm = screen.getByRole('button', { name: /confirm/i });

    // Forward-tab from the last element → should jump to the first.
    confirm.focus();
    fireEvent.keyDown(screen.getByRole('dialog'), { key: 'Tab' });
    expect(document.activeElement).toBe(cancel);
  });

  it('Shift+Tab from the first focusable wraps to the last (focus trap)', async () => {
    renderDialog();
    await new Promise((r) => setTimeout(r, 5));
    const cancel = screen.getByRole('button', { name: /cancel/i });
    const confirm = screen.getByRole('button', { name: /confirm/i });

    cancel.focus();
    fireEvent.keyDown(screen.getByRole('dialog'), { key: 'Tab', shiftKey: true });
    expect(document.activeElement).toBe(confirm);
  });

  it('busy state disables both buttons and shows "Working…"', () => {
    renderDialog({ busy: true });
    expect(screen.getByRole('button', { name: /working…/i })).toBeDisabled();
    expect(screen.getByRole('button', { name: /cancel/i })).toBeDisabled();
  });
});

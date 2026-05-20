// CodeDisplay copy-to-clipboard + accessible labeling.

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { axe } from 'jest-axe';
import CodeDisplay from './CodeDisplay';

describe('<CodeDisplay />', () => {
  const originalClipboard = (global.navigator as { clipboard?: Clipboard }).clipboard;

  beforeEach(() => {
    Object.defineProperty(global.navigator, 'clipboard', {
      value: { writeText: vi.fn().mockResolvedValue(undefined) },
      configurable: true,
    });
  });
  afterEach(() => {
    Object.defineProperty(global.navigator, 'clipboard', {
      value: originalClipboard,
      configurable: true,
    });
  });

  it('renders the code with an accessible label and copies on click', async () => {
    const { container } = render(<CodeDisplay code="123456" />);
    // aria-label exposes the full code with spaces so SR reads digits individually.
    expect(screen.getByLabelText(/linking code 1 2 3 4 5 6/i)).toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: /copy code to clipboard/i }));
    await waitFor(() =>
      expect(
        (global.navigator as unknown as { clipboard: { writeText: ReturnType<typeof vi.fn> } }).clipboard.writeText,
      ).toHaveBeenCalledWith('123456'),
    );
    await waitFor(() =>
      expect(screen.getByRole('button', { name: /code copied to clipboard/i })).toBeInTheDocument(),
    );

    const results = await axe(container);
    expect(results).toHaveNoViolations();
  });

  it('falls back to an inline error when clipboard API is unavailable', async () => {
    Object.defineProperty(global.navigator, 'clipboard', {
      value: undefined,
      configurable: true,
    });
    render(<CodeDisplay code="654321" />);
    fireEvent.click(screen.getByRole('button', { name: /copy code/i }));
    await waitFor(() =>
      expect(screen.getByRole('alert')).toHaveTextContent(/clipboard unavailable/i),
    );
  });
});

// SearchInput debounce behaviour.

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { fireEvent, render, screen } from '@testing-library/react';
import SearchInput from './SearchInput';

describe('<SearchInput />', () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it('debounces onChange by the configured window', () => {
    const onChange = vi.fn();
    render(
      <SearchInput
        value=""
        onChange={onChange}
        ariaLabel="Search"
        debounceMs={200}
      />,
    );
    const input = screen.getByRole('searchbox', { name: /search/i });
    fireEvent.change(input, { target: { value: 'h' } });
    fireEvent.change(input, { target: { value: 'he' } });
    fireEvent.change(input, { target: { value: 'hel' } });
    // Within the window — nothing emitted yet.
    expect(onChange).not.toHaveBeenCalled();
    vi.advanceTimersByTime(199);
    expect(onChange).not.toHaveBeenCalled();
    vi.advanceTimersByTime(2);
    expect(onChange).toHaveBeenCalledTimes(1);
    expect(onChange).toHaveBeenCalledWith('hel');
  });

  it('updates the visible draft on every keystroke', () => {
    render(<SearchInput value="" onChange={() => undefined} ariaLabel="Search" />);
    const input = screen.getByRole('searchbox', { name: /search/i }) as HTMLInputElement;
    fireEvent.change(input, { target: { value: 'foo' } });
    expect(input.value).toBe('foo');
  });

  it('external value reset to empty string overwrites the draft', () => {
    const { rerender } = render(
      <SearchInput value="initial" onChange={() => undefined} ariaLabel="Search" />,
    );
    const input = screen.getByRole('searchbox', { name: /search/i }) as HTMLInputElement;
    expect(input.value).toBe('initial');
    rerender(<SearchInput value="" onChange={() => undefined} ariaLabel="Search" />);
    expect(input.value).toBe('');
  });
});

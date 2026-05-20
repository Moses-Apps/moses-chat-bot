// Range slider with live value display.
//
// - Wraps the native <input type="range">; no third-party slider lib.
// - Renders the current value next to the label so the user knows what each
//   tick does without dragging.
// - 44px-tall touch target via min-h on the wrapping label.

import { useId, type ReactElement, type ReactNode } from 'react';

interface SliderProps {
  value: number;
  onChange: (next: number) => void;
  min: number;
  max: number;
  step?: number;
  /** Visible label rendered above the slider. */
  label: ReactNode;
  /** Optional helper text rendered below the slider. */
  description?: ReactNode;
  /** Suffix appended to the value display (e.g. ' h', ' agents'). */
  unit?: string;
}

export function Slider({
  value,
  onChange,
  min,
  max,
  step = 1,
  label,
  description,
  unit,
}: SliderProps): ReactElement {
  const id = useId();
  return (
    <div className="py-2">
      <div className="flex items-baseline justify-between gap-4">
        <label htmlFor={id} className="text-sm font-medium text-moses-text">
          {label}
        </label>
        <span
          aria-live="polite"
          className="text-sm tabular-nums font-semibold text-moses-accent"
        >
          {value}
          {unit}
        </span>
      </div>
      <input
        id={id}
        type="range"
        min={min}
        max={max}
        step={step}
        value={value}
        onChange={(e) => onChange(Number(e.target.value))}
        className="mt-2 w-full min-h-[44px] cursor-pointer accent-moses-accent focus:outline-none focus:ring-2 focus:ring-moses-accent/40"
      />
      {description && (
        <p className="mt-1 text-xs text-moses-text-muted">{description}</p>
      )}
    </div>
  );
}

export default Slider;

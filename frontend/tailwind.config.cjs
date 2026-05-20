/** @type {import('tailwindcss').Config} */
// 4px-grid spacing scale; default scale otherwise.
// Moses palette: slate-based dark with blue-500 accents.
// darkMode: 'media' honors the user's OS preference (matches Moses platform).
const spacing = {};
for (let i = 0; i <= 32; i += 1) {
  spacing[`space-${i}`] = `${i * 4}px`;
}

module.exports = {
  darkMode: 'media',
  content: ['./index.html', './src/**/*.{ts,tsx}'],
  theme: {
    extend: {
      fontFamily: {
        sans: ['Inter', 'ui-sans-serif', 'system-ui', '-apple-system', 'sans-serif'],
      },
      spacing: {
        // Existing 4px-aligned tokens (kept for backwards-compat with App.tsx)
        '0.5': '2px',
        '1': '4px',
        '2': '8px',
        '3': '12px',
        '4': '16px',
        '5': '20px',
        '6': '24px',
        '8': '32px',
        '10': '40px',
        '12': '48px',
        '16': '64px',
        // Explicit `space-N` scale (4px * N, N=0..32) for grid-aligned layouts.
        ...spacing,
      },
      colors: {
        moses: {
          // Slate-based surface palette
          surface: {
            DEFAULT: '#f8fafc', // slate-50
            raised: '#ffffff',
            sunken: '#f1f5f9',  // slate-100
            dark: '#0f172a',    // slate-900
            'dark-raised': '#1e293b', // slate-800
            'dark-sunken': '#020617', // slate-950
          },
          border: {
            DEFAULT: '#e2e8f0', // slate-200
            dark: '#334155',    // slate-700
          },
          text: {
            DEFAULT: '#0f172a', // slate-900
            muted: '#475569',   // slate-600
            subtle: '#94a3b8',  // slate-400
            inverse: '#f8fafc', // slate-50
          },
          // Blue-500 accent family
          accent: {
            DEFAULT: '#3b82f6', // blue-500
            hover: '#2563eb',   // blue-600
            soft: '#dbeafe',    // blue-100
          },
          // Status tokens
          status: {
            active: '#10b981',   // emerald-500
            inactive: '#94a3b8', // slate-400
            pending: '#f59e0b',  // amber-500
            error: '#ef4444',    // red-500
          },
        },
      },
      borderRadius: {
        bento: '12px',
      },
      boxShadow: {
        bento: '0 1px 2px 0 rgb(15 23 42 / 0.04), 0 1px 3px 0 rgb(15 23 42 / 0.06)',
        'bento-hover': '0 4px 6px -1px rgb(15 23 42 / 0.08), 0 2px 4px -2px rgb(15 23 42 / 0.06)',
      },
    },
  },
  plugins: [],
};

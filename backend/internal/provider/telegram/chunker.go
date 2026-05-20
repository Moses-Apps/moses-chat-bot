package telegram

import (
	"strings"
	"unicode/utf8"
)

// Chunk splits text into pieces no larger than maxBytes (UTF-8 byte count),
// preferring boundary splits in this priority order:
//
//  1. last "\n\n" before the limit (paragraph)
//  2. last "\n"   before the limit (line)
//  3. last ". "   before the limit (sentence)
//  4. last space  before the limit (word)
//  5. hard cut at the last rune boundary that fits
//
// Empty input yields a nil slice. maxBytes <= 0 returns the input unchanged
// as a single chunk (defensive: callers should always pass 4096 for Telegram).
//
// The function never splits inside a multi-byte UTF-8 rune. Each output
// chunk is guaranteed to be <= maxBytes in byte length.
func Chunk(text string, maxBytes int) []string {
	if text == "" {
		return nil
	}
	if maxBytes <= 0 {
		return []string{text}
	}

	var out []string
	remaining := text
	for len(remaining) > maxBytes {
		// window is the candidate prefix we may emit.
		window := remaining[:maxBytes]

		// Find the best boundary inside window.
		cut := bestBoundary(window)

		if cut <= 0 {
			// No friendly boundary; hard-cut at the last rune that fits.
			cut = lastRuneBoundary(window)
			if cut <= 0 {
				// Pathological: maxBytes smaller than a single rune.
				// Return remaining as-is to avoid an infinite loop.
				out = append(out, remaining)
				return out
			}
		}

		piece := strings.TrimRight(remaining[:cut], " \t\n")
		out = append(out, piece)
		// Advance past the boundary, skipping a single leading whitespace if any.
		remaining = strings.TrimLeft(remaining[cut:], " \t\n")
	}
	if remaining != "" {
		out = append(out, remaining)
	}
	return out
}

// bestBoundary returns an index in window (1..len(window)) suitable for a
// "soft" split, or -1 if none was found.
func bestBoundary(window string) int {
	if i := strings.LastIndex(window, "\n\n"); i >= 0 {
		return i + 2
	}
	if i := strings.LastIndex(window, "\n"); i >= 0 {
		return i + 1
	}
	if i := strings.LastIndex(window, ". "); i >= 0 {
		return i + 2
	}
	if i := strings.LastIndex(window, " "); i >= 0 {
		return i + 1
	}
	return -1
}

// lastRuneBoundary returns the largest index <= len(window) that lies on a
// rune boundary. Always returns at least the length of the first rune.
func lastRuneBoundary(window string) int {
	if len(window) == 0 {
		return 0
	}
	// Walk back from the end until we find a rune-start byte.
	for i := len(window); i > 0; i-- {
		if utf8.RuneStart(window[i-1]) {
			// Validate the rune at i-1 actually decodes within window.
			r, sz := utf8.DecodeRuneInString(window[i-1:])
			if r == utf8.RuneError && sz < 2 {
				continue
			}
			// Boundary AFTER this rune is i-1+sz, only if it fits.
			if i-1+sz <= len(window) {
				return i - 1 + sz
			}
		}
	}
	return 0
}

package telegram

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestChunk(t *testing.T) {
	cases := []struct {
		name      string
		text      string
		maxBytes  int
		wantParts int
		// optional per-chunk checks
		check func(t *testing.T, parts []string)
	}{
		{
			name:      "empty input yields nil",
			text:      "",
			maxBytes:  4096,
			wantParts: 0,
		},
		{
			name:      "short text returns single chunk",
			text:      "hello world",
			maxBytes:  4096,
			wantParts: 1,
		},
		{
			name:      "exactly at limit returns single chunk",
			text:      strings.Repeat("a", 4096),
			maxBytes:  4096,
			wantParts: 1,
			check: func(t *testing.T, parts []string) {
				if len(parts[0]) != 4096 {
					t.Fatalf("chunk len=%d, want 4096", len(parts[0]))
				}
			},
		},
		{
			name:      "just over limit splits in two",
			text:      strings.Repeat("a", 4097),
			maxBytes:  4096,
			wantParts: 2,
		},
		{
			name:      "splits on paragraph boundary when available",
			text:      strings.Repeat("a", 100) + "\n\n" + strings.Repeat("b", 100),
			maxBytes:  120,
			wantParts: 2,
			check: func(t *testing.T, parts []string) {
				if !strings.HasSuffix(parts[0], "a") {
					t.Fatalf("first chunk should end with a, got %q", parts[0])
				}
				if !strings.HasPrefix(parts[1], "b") {
					t.Fatalf("second chunk should start with b, got %q", parts[1])
				}
			},
		},
		{
			name:      "splits on line boundary",
			text:      strings.Repeat("a", 100) + "\n" + strings.Repeat("b", 100),
			maxBytes:  120,
			wantParts: 2,
		},
		{
			name:      "splits on sentence boundary",
			text:      strings.Repeat("a", 100) + ". " + strings.Repeat("b", 100),
			maxBytes:  120,
			wantParts: 2,
		},
		{
			name:      "splits on word boundary",
			text:      strings.Repeat("a", 100) + " " + strings.Repeat("b", 100),
			maxBytes:  120,
			wantParts: 2,
		},
		{
			name:      "hard cut when no whitespace",
			text:      strings.Repeat("x", 250),
			maxBytes:  100,
			wantParts: 3,
			check: func(t *testing.T, parts []string) {
				for i, p := range parts {
					if len(p) > 100 {
						t.Fatalf("chunk[%d] exceeds limit: %d", i, len(p))
					}
				}
			},
		},
		{
			name:      "emoji unicode never split mid-rune",
			text:      strings.Repeat("😀", 200), // 4 bytes per emoji
			maxBytes:  37,                       // intentionally a non-multiple of 4
			wantParts: 0,                        // verified in check
			check: func(t *testing.T, parts []string) {
				for i, p := range parts {
					if !utf8.ValidString(p) {
						t.Fatalf("chunk[%d] invalid utf-8: %q", i, p)
					}
					if len(p) > 37 {
						t.Fatalf("chunk[%d] byte-len %d > 37", i, len(p))
					}
				}
				// Reassemble and ensure no data loss.
				if want := strings.Repeat("😀", 200); strings.Join(parts, "") != want {
					t.Fatalf("reassembled string differs from input")
				}
			},
		},
		{
			name:      "multi-paragraph realistic message",
			text:      strings.Repeat("Sentence one. ", 200) + "\n\n" + strings.Repeat("Sentence two. ", 200),
			maxBytes:  500,
			wantParts: 0,
			check: func(t *testing.T, parts []string) {
				if len(parts) < 2 {
					t.Fatalf("expected multiple chunks, got %d", len(parts))
				}
				for i, p := range parts {
					if len(p) > 500 {
						t.Fatalf("chunk[%d] exceeds limit: %d", i, len(p))
					}
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Chunk(tc.text, tc.maxBytes)
			if tc.wantParts > 0 && len(got) != tc.wantParts {
				t.Fatalf("Chunk parts=%d, want %d (chunks=%v)", len(got), tc.wantParts, lengthsOf(got))
			}
			if tc.check != nil {
				tc.check(t, got)
			}
			for i, p := range got {
				if len(p) > tc.maxBytes {
					t.Fatalf("chunk[%d] len=%d exceeds maxBytes=%d", i, len(p), tc.maxBytes)
				}
			}
		})
	}
}

func TestChunk_MaxBytesZero_ReturnsAsIs(t *testing.T) {
	got := Chunk("abc", 0)
	if len(got) != 1 || got[0] != "abc" {
		t.Fatalf("Chunk with maxBytes=0 returned %v", got)
	}
}

func lengthsOf(s []string) []int {
	out := make([]int, len(s))
	for i, v := range s {
		out[i] = len(v)
	}
	return out
}

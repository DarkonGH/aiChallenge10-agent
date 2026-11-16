package main

import "testing"

// The reasoning block is erased by moving the cursor up exactly as many lines
// as wrapReason reported, so the count must match the printed text: a line of
// exactly `width` cells is one screen line, the next rune starts a new one.
func TestWrapReasonLines(t *testing.T) {
	cases := []struct {
		name     string
		col      int
		width    int
		in       string
		wantText string
		wantCol  int
		wantDown int
	}{
		{"short line", 0, 10, "abc", "abc", 3, 0},
		{"exactly full line", 0, 10, "abcdefghij", "abcdefghij", 10, 0},
		{"one rune past the width", 0, 10, "abcdefghijk", "abcdefghij\nk", 1, 1},
		{"continues a full line", 10, 10, "k", "\nk", 1, 1},
		{"model newline", 0, 10, "ab\ncd", "ab\ncd", 2, 1},
		{"carriage return dropped", 0, 10, "ab\rcd", "abcd", 4, 0},
		{"tab becomes a space", 0, 10, "a\tb", "a b", 3, 0},
		{"wrap then newline", 0, 10, "abcdefghijk\n", "abcdefghij\nk\n", 0, 2},
		{"empty chunk", 4, 10, "", "", 4, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			text, col, down := wrapReason(c.col, c.width, c.in)
			if text != c.wantText || col != c.wantCol || down != c.wantDown {
				t.Errorf("wrapReason(%d, %d, %q) = (%q, %d, %d), want (%q, %d, %d)",
					c.col, c.width, c.in, text, col, down, c.wantText, c.wantCol, c.wantDown)
			}
		})
	}
}

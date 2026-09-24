package router

import (
	"strings"
	"testing"
)

func TestLiveDiffMouseReports(t *testing.T) {
	for _, tc := range []struct {
		report string
		want   byte
	}{
		{"<67;10;5M", 0}, {"<66;10;5M", 0},
		{"<64;10;5M", 'k'}, {"<65;10;5M", 'j'},
		{"<35;10;5M", 'h'},
		{"<95;10;5M", 0}, // All three modifiers.
		{"<67;10;5m", 0}, {"<0;10;5M", '\r'}, {"<99;10;5M", 0},
		{"<67;0;5M", 0}, {"<67;10;0M", 0}, {"<67;1M", 0},
		{"<67;1;2;3M", 0}, {"<67;-1;5M", 0},
		{"<67;fFqr;5M", 0}, {"<999999999999999999999999999;1;1M", 0},
		{"<67;" + strings.Repeat("1", 100) + ";5M", 0},
	} {
		t.Run(tc.report, func(t *testing.T) {
			var mouse liveDiffMouse
			for i, key := range []byte(tc.report) {
				want := byte(0)
				if i == len(tc.report)-1 {
					want = tc.want
				}
				got, row, column := mouse.consume(key)
				if got != want {
					t.Fatalf("byte %d: got %q, want %q", i, got, want)
				}
				if got != 0 && (row != 5 || column != 10) || got == 0 && (row != 0 || column != 0) {
					t.Fatalf("byte %d: unexpected mouse row %d", i, row)
				}
				if len(mouse.payload) > 48 {
					t.Fatal("mouse storage exceeded bound")
				}
			}
			if mouse != (liveDiffMouse{}) {
				t.Fatalf("report did not reset decoder: %+v", mouse)
			}
		})
	}
}

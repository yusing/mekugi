package logicalrow

import (
	"testing"
)

func TestCountAndAt(t *testing.T) {
	tests := []struct {
		name string
		text string
		want []Line
	}{
		{name: "empty"},
		{name: "one", text: "alpha", want: []Line{{Start: 0, ContentEnd: 5, End: 5}}},
		{name: "final LF", text: "alpha\n", want: []Line{{Start: 0, ContentEnd: 5, End: 6}}},
		{
			name: "mixed terminators",
			text: "a\r\nb\rc\n\n",
			want: []Line{
				{Start: 0, ContentEnd: 1, End: 3},
				{Start: 3, ContentEnd: 4, End: 5},
				{Start: 5, ContentEnd: 6, End: 7},
				{Start: 7, ContentEnd: 7, End: 8},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := Count(test.text); got != len(test.want) {
				t.Fatalf("Count(%q) = %d, want %d", test.text, got, len(test.want))
			}
			for index, want := range test.want {
				got, ok := At(test.text, index+1)
				if !ok || got != want {
					t.Fatalf("At(%q,%d) = %#v, %v; want %#v", test.text, index+1, got, ok, want)
				}
			}
			if got, ok := At(test.text, len(test.want)+1); ok || got != (Line{}) {
				t.Fatalf("out-of-range At = %#v, %v", got, ok)
			}
		})
	}
}

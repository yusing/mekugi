package logicalrow

import (
	"testing"
)

func TestLines(t *testing.T) {
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
			got := Lines(test.text)
			if len(got) != len(test.want) {
				t.Fatalf("Lines(%q) = %#v, want %#v", test.text, got, test.want)
			}
			for index := range got {
				if got[index] != test.want[index] {
					t.Fatalf("Lines(%q)[%d] = %#v, want %#v", test.text, index, got[index], test.want[index])
				}
			}
		})
	}
}

package verifiedrow

import (
	"errors"
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

func TestHash(t *testing.T) {
	if got := Hash("hello"); got != "2cf2" {
		t.Fatalf("Hash(hello) = %q, want 2cf2", got)
	}
	if got := Hash16([]byte("hello")); got != 0x2cf2 {
		t.Fatalf("Hash16(hello) = %#x, want 0x2cf2", got)
	}
}

func TestParseReference(t *testing.T) {
	for _, test := range []struct {
		input string
		want  Reference
		err   error
	}{
		{"1:0000", Reference{1, "0000"}, nil},
		{"18446744073709551615:abcd", Reference{18446744073709551615, "abcd"}, nil},
		{"18446744073709551616:abcd", Reference{}, ErrLineOutOfRange},
		{"999999999999999999999999999999x:abcd", Reference{}, ErrInvalidReference},
		{"", Reference{}, ErrInvalidReference},
		{":abcd", Reference{}, ErrInvalidReference},
		{"0:abcd", Reference{}, ErrInvalidReference},
		{"01:abcd", Reference{}, ErrInvalidReference},
		{"+1:abcd", Reference{}, ErrInvalidReference},
		{"-1:abcd", Reference{}, ErrInvalidReference},
		{"1_0:abcd", Reference{}, ErrInvalidReference},
		{"１:abcd", Reference{}, ErrInvalidReference},
		{"1:ABCd", Reference{}, ErrInvalidReference},
		{"1:abc", Reference{}, ErrInvalidReference},
		{"1:abcde", Reference{}, ErrInvalidReference},
		{"1:abcd:", Reference{}, ErrInvalidReference},
		{"1:ab:d", Reference{}, ErrInvalidReference},
		{"1:abcg", Reference{}, ErrInvalidReference},
		{" 1:abcd", Reference{}, ErrInvalidReference},
		{"1:abcd\n", Reference{}, ErrInvalidReference},
		{"1:abcd\r", Reference{}, ErrInvalidReference},
		{"1:abcd\x00", Reference{}, ErrInvalidReference},
	} {
		got, err := ParseReference(test.input)
		if got != test.want || !errors.Is(err, test.err) {
			t.Errorf("ParseReference(%q) = %+v, %v; want %+v, %v", test.input, got, err, test.want, test.err)
		}
	}
}

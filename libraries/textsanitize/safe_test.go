package textsanitize

import "testing"

func TestSafeText(t *testing.T) {
	got := Safe("\x1b]52;c;clipboard\a\x1b[2J\x1b[31mred\x1b[0m\t界\r\n", true)
	if got != "\x1b[31mred\x1b[0m    界\n" {
		t.Fatalf("unsafe rendering: %q", got)
	}
}

func TestSafePreservesZeroWidthText(t *testing.T) {
	const source = "e\u0301 界 👩\u200d💻 क\u094d\u200cष ✈\ufe0f"
	for _, colors := range []bool{false, true} {
		got := Safe("\x1b]52;c;clipboard\a\x1b[2J"+source+"\x00\x1b[6n\n", colors)
		if got != source+"\n" {
			t.Fatalf("colors=%t: source text damaged or terminal controls retained: %q", colors, got)
		}
	}
}

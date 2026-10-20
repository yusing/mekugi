package policy

import "testing"

func TestStripInstructionOmissions(t *testing.T) {
	block := instructionOmitStart + "use rtk" + instructionOmitEnd
	for _, test := range []struct{ name, input, want string }{
		{"plain", "keep", "keep"},
		{"block", "before\n" + block + "\nafter", "before\n\nafter"},
		{"multiple", block + "keep" + block, "keep"},
		{"empty", instructionOmitStart + instructionOmitEnd, ""},
		{"nested", "a" + instructionOmitStart + block + "outer" + instructionOmitEnd + "b", "ab"},
		{"unclosed", "a" + instructionOmitStart + "keep", "a" + instructionOmitStart + "keep"},
		{"orphan close", instructionOmitEnd + "keep", instructionOmitEnd + "keep"},
		{"unclosed outer", instructionOmitStart + block, instructionOmitStart + block},
		{"complete then unclosed", block + instructionOmitStart + "keep", instructionOmitStart + "keep"},
		{"fenced", "```\n" + block + "\n```", "```\n\n```"},
		{"crlf", "a\r\n" + block + "\r\nb", "a\r\n\r\nb"},
		{"near match", "<!-- mekugi:omit-->keep<!-- /mekugi:omit-->", "<!-- mekugi:omit-->keep<!-- /mekugi:omit-->"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := Omissions(test.input)
			if got != test.want {
				t.Fatalf("got %q, want %q", got, test.want)
			}
			if again := Omissions(got); again != got {
				t.Fatalf("not idempotent: %q", again)
			}
		})
	}
}

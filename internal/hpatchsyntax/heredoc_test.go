package hpatchsyntax

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestHeredocDelimitersAndLiteralBodies(t *testing.T) {
	for _, ending := range []string{"\n", "\r\n"} {
		for _, marker := range []string{"<<END", "<<'END'", `<<"END"`, "<< END", "<<-END", "<<- 'END'"} {
			payload := "type <<PATCH" + ending + "PATCH" + ending + "|body" + ending + "$HOME `date` $(date)" + ending + ending
			header := `type "old" ` + marker
			lines := SplitPhysicalLines(header + ending + payload + "END" + ending + "rm")
			frame, err := FrameCommand(lines, 0, header)
			if err != nil || frame.Marker != marker || frame.Delimiter != "END" || frame.Body != payload || lines[frame.Next].Text != "rm" {
				t.Fatalf("frame = %+v, error %v; want %q followed by rm", frame, err, payload)
			}
		}
	}
}

func TestHeredocBoundaries(t *testing.T) {
	for _, tc := range []struct{ marker, body, want, failure string }{
		{"<<END", "END\n", "", ""},
		{"<<END", "\nEND", "\n", ""},
		{"<<END", "one\r\ntwo\nEND\n", "one\r\ntwo\n", ""},
		{"<<-END", "\t\tone\n \ttwo\n\tEND\n", "one\n \ttwo\n", ""},
		{"<<END", "END\x00\nbody\nEND\n", "END\x00\nbody\n", ""},
		{"<<END", "END\r\r\nbody\r\nEND\r\n", "END\r\r\nbody\r\n", ""},
		{"<<END", "body\nEND\r", "body\n", ""},
		{"<<END", "$(incomplete\n`unterminated\n${bad\n\\\nEND\n", "$(incomplete\n`unterminated\n${bad\n\\\n", ""},
		{"<<E'N'D", "body\nEND\n", "body\n", ""},
		{"<<\\END", "body\nEND\n", "body\n", ""},
		{"<<$HOME", "body\n$HOME\n", "body\n", ""},
		{"<<'$HOME'", "body\n$HOME\n", "body\n", ""},
		{"<<END", "body\nEND\nnot valid shell )\n", "body\n", ""},

		{"<<END", "\tone\nEND\n", "\tone\n", ""},
		{"<<'\tEND'", "one\n\tEND\n", "one\n", ""},
		{`<<"a'$b"`, "one\na'$b\n", "one\n", ""},
		{`<<"a'\b"`, "one\na'\\b\n", "one\n", ""},
		{"<<x", "\x00\nx\n", "\x00\n", ""},
		{"<<" + strings.Repeat("D", 1024), strings.Repeat("\x00\n", 1024) + strings.Repeat("D", 1024) + "\n", strings.Repeat("\x00\n", 1024), ""},

		{"<<END\\ ", "one\nEND \n", "one\n", ""},
		{"<<END\\ \t", "one\nEND \n", "one\n", ""},
		{"<<END\\\t", "one\nEND\t\n", "one\n", ""},
		{"<<END\\\t ", "one\nEND\t\n", "one\n", ""},

		{"<<'END HERE'", "one\nEND HERE\n", "one\n", ""},
		{"<<PATCH-", "one\nPATCH-\n", "one\n", ""},
		{"<<TEXT", "|body\nTEXT\n", "|body\n", ""},
		{"<<END", "one\nWRONG\nrm\n", "", "unterminated heredoc"},
		{"<<END", "one\nEND extra\n", "", "unterminated heredoc"},
		{"<<END", "one\n END\n", "", "unterminated heredoc"},
		{"<<END", string([]byte{0xff}) + "\nEND\n", "", "not UTF-8"},
		{"<<END", strings.Repeat("x", MaxHeredocBodyBytes-1) + "\nEND\n", strings.Repeat("x", MaxHeredocBodyBytes-1) + "\n", ""},
		{"<<END", strings.Repeat("x", MaxHeredocBodyBytes) + "\nEND\n", "", "body exceeds"},
		{"<<", "", "", "invalid heredoc"},
		{"<<END extra", "", "", "invalid heredoc"},
		{"<<'END", "", "", "invalid heredoc"},
		{"<<END ; touch side_effect", "", "", "invalid heredoc"},
		{"<<END >other", "", "", "invalid heredoc"},
		{"<<END #comment", "", "", "invalid heredoc"},
		{"<<$(date)", "", "", "invalid heredoc"},
		{"<<EN\x00D", "", "", "invalid heredoc"},

		{"<<''", "", "", "invalid heredoc"},
	} {
		t.Run(tc.marker+"/"+tc.failure, func(t *testing.T) {
			header := "type " + tc.marker
			lines := SplitPhysicalLines(header + "\n" + tc.body)
			frame, err := FrameCommand(lines, 0, header)
			if tc.failure != "" {
				if err == nil || !strings.Contains(err.Error(), tc.failure) || frame.Body != "" {
					t.Fatalf("frame bytes %d, error %v; want %q", len(frame.Body), err, tc.failure)
				}
			} else if err != nil || frame.Body != tc.want {
				t.Fatalf("frame bytes %d, error %v; want %d bytes", len(frame.Body), err, len(tc.want))
			}
		})
	}
}

func TestHeredocShellParserPreservesPhysicalRows(t *testing.T) {
	for _, ending := range []string{"\n", "\r\n"} {
		for _, payload := range []string{
			"", "\x00", "END\x00", "END\r", "END\r\r", "\\", "\\\\",
			"$(unterminated", "${unterminated", "`unterminated", "\tEND ",
			"quotes '\" and \uFFFD 世界", "shell touch never", "type <<END",
		} {
			if payload == "END\r" && ending == "\n" {
				continue // Together these form an actual CRLF closing line.
			}
			header := "type <<'END'"
			lines := SplitPhysicalLines("new a\n" + header + ending + payload + ending + "END" + ending + "rm")
			frame, err := FrameCommand(lines, 1, header)
			if err != nil || frame.Next != 4 || frame.Body != payload+ending || lines[frame.Next].Text != "rm" {
				t.Fatalf("payload %q ending %q: frame=%+v error=%v", payload, ending, frame, err)
			}
		}
	}
}

func FuzzHeredocLiteralBody(f *testing.F) {
	for _, body := range []string{"plain", "END\x00\nbody", "END\r", "\\\n", "$(unclosed", "世界"} {
		f.Add(body)
	}
	f.Fuzz(func(t *testing.T, body string) {
		if len(body) > 4096 || !utf8.ValidString(body) {
			t.Skip()
		}
		const delimiter = "MEKUGI_END"
		for _, line := range SplitPhysicalLines(body + "\n") {
			if line.Text == delimiter {
				t.Skip()
			}
		}
		header := "type <<'" + delimiter + "'"
		lines := SplitPhysicalLines(header + "\n" + body + "\n" + delimiter + "\nrm")
		frame, err := FrameCommand(lines, 0, header)
		if err != nil || frame.Body != body+"\n" || lines[frame.Next].Text != "rm" {
			t.Fatalf("frame=%+v err=%v body=%q", frame, err, body)
		}
	})
}

func FuzzHeredocDelimiterRoundTrip(f *testing.F) {
	for _, delimiter := range []string{"END", "x", "A\tB", "a'$b", "a'\\b", "\uFFFD世界", "a'b\"c"} {
		f.Add(delimiter)
	}
	f.Fuzz(func(t *testing.T, delimiter string) {
		if delimiter == "" || len(delimiter) > 4096 || !utf8.ValidString(delimiter) || strings.ContainsAny(delimiter, "\x00\r\n") {
			t.Skip()
		}
		quoted := "<<'" + strings.ReplaceAll(delimiter, "'", "'\\''") + "'"
		var escaped strings.Builder
		escaped.WriteString("<<")
		for _, character := range delimiter {
			escaped.WriteByte('\\')
			escaped.WriteRune(character)
		}
		body := "payload"
		if body == delimiter {
			body = "other"
		}
		for _, marker := range []string{quoted, escaped.String()} {
			header := "type " + marker
			lines := SplitPhysicalLines(header + "\n" + body + "\n" + delimiter + "\nrm")
			frame, err := FrameCommand(lines, 0, header)
			if err != nil || frame.Delimiter != delimiter || frame.Body != body+"\n" || frame.Next != 3 {
				t.Fatalf("delimiter=%q frame=%+v err=%v", delimiter, frame, err)
			}
		}
	})
}

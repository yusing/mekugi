package terminal

import (
	"testing"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

func TestTruncate(t *testing.T) {
	for _, tc := range []struct {
		path  string
		width int
		want  string
	}{
		{"/a/b.go", 0, ""},
		{"/a/b.go", -1, ""},
		{"/a/b.go", 1, "…"},
		{"/a/b.go", 2, "…o"},
		{"/a/b.go", 7, "/a/b.go"},
		{"/a/b.go", 6, "…/b.go"},
		{"directory/long_filename.go", 12, "…filename.go"},
		{"long_filename.go", 8, "…name.go"},
		{`C:\directory\file.go`, 8, `…file.go`},
		{"/external/a directory/", 11, "…directory/"},
		{"/src/界面.go", 7, "…面.go"},
		{"/src/界面.go", 6, "…面.go"},
		{"/src/e\u0301.go", 5, "…e\u0301.go"},
		{"/src/👩‍💻.go", 6, "…👩‍💻.go"},
		{"/src/界", 2, "…"},
		{"/src/👩‍💻", 2, "…"},
		{"\x1b[2m/src/\x1b[22m\x1b[1mfile.go\x1b[22m", 8, "…file.go"},
	} {
		t.Run(tc.path+"/"+tc.want, func(t *testing.T) {
			got := Truncate(tc.path, tc.width)
			if ansi.Strip(got) != tc.want || !utf8.ValidString(got) || ansi.StringWidth(got) > max(0, tc.width) {
				t.Fatalf("Truncate(%q, %d) = %q, want %q", tc.path, tc.width, got, tc.want)
			}
		})
	}
}

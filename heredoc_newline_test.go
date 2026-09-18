package mekugi

import (
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/patchtest"
)

func TestExplicitHeredocNewlineBoundaries(t *testing.T) {
	for _, test := range []struct {
		name   string
		before string
		edit   string
		want   string
	}{
		{
			name: "literal paragraph without extra blank", before: "old\n\nnext\n",
			edit: "type \"old\" \"first\\nlast\"\n",
			want: "first\nlast\n\nnext\n",
		},
		{
			name: "ordinary heredoc stays byte exact", before: "old\n\nnext\n",
			edit: "type \"old\" <<END\nfirst\nlast\nEND\n",
			want: "first\nlast\n\n\nnext\n",
		},
		{
			name: "anchored mid-line", before: "before old after\n",
			edit: "type " + row(1, "before old after") + " \"old\" \"first\\nlast\"\n",
			want: "before first\nlast after\n",
		},
		{
			name: "multiple literals", before: "old old\n",
			edit: "type \"old\" 2 \"new\"\n",
			want: "new new\n",
		},
		{
			name: "line still preserves owned terminator", before: "old\nnext\n",
			edit: "type " + row(1, "old") + " \"new\"\n",
			want: "new\nnext\n",
		},
		{
			name: "range preserves CRLF", before: "old\r\nlast\r\nnext\r\n",
			edit: "type " + row(1, "old") + ".." + row(2, "last") + " \"new\"\n",
			want: "new\r\nnext\r\n",
		},
		{
			name: "unterminated last row", before: "old",
			edit: "type " + row(1, "old") + " \"new\"\n",
			want: "new",
		},
		{
			name: "empty quoted value deletes row", before: "old\nnext\n",
			edit: "type " + row(1, "old") + " \"\"\n",
			want: "next\n",
		},
		{
			name: "inline insertion", before: "left right\n",
			edit: "add \"right\" \"middle \"\n",
			want: "left middle right\n",
		},
		{
			name: "insert before existing blank", before: "before\n\nnext\n",
			edit: "add " + row(2, "") + " <<END\ninserted\nEND\n",
			want: "before\ninserted\n\nnext\n",
		},
		{
			name: "EOF without terminator", before: "before\n",
			edit: `append "after"`,
			want: "before\nafter",
		},
		{
			name: "explicit blank retained", before: "old\nnext\n",
			edit: "type \"old\" <<END\nnew\nEND\n",
			want: "new\n\nnext\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, root, "file.txt", test.before, 0o644)
			edits := []FileEdit{{Path: "file.txt", Script: test.edit}}
			translation, err := translateForHostAtTest(t, root, edits, "")
			if err != nil {
				t.Fatalf("translate: %v, %s", err, translation.Diagnostic)
			}
			// The patch harness is LF-only; exact CRLF ownership is checked
			// through Apply below.
			normalized := strings.ReplaceAll(test.before, "\r\n", "\n")
			wantTranslated := strings.ReplaceAll(test.want, "\r\n", "\n")
			if wantTranslated != "" && !strings.HasSuffix(wantTranslated, "\n") {
				wantTranslated += "\n" // Native apply_patch cannot leave the final line unterminated.
			}
			tree, err := patchtest.Apply(map[string]string{"file.txt": normalized}, string(translation.Patch))
			if err != nil || tree["file.txt"] != wantTranslated {
				t.Fatalf("translated content = %q, error %v, want %q", tree["file.txt"], err, wantTranslated)
			}
			result, err := applyForHostAtTest(t, root, edits, "")
			if err != nil {
				t.Fatalf("apply: %v, %s", err, result.Diagnostic)
			}
			if got := readTestFile(t, root, "file.txt"); got != test.want {
				t.Fatalf("content = %q, want %q", got, test.want)
			}
		})
	}
}
func TestQuotedInitializerWithoutFinalNewline(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "file.txt", "", 0o644)
	result, err := applyForHostAtTest(t, root, []FileEdit{{Path: "file.txt", Script: `append "value \t"`}}, "")
	if err != nil {
		t.Fatalf("apply: %v, %s", err, result.Diagnostic)
	}
	if got := readTestFile(t, root, "file.txt"); got != "value \t" {
		t.Fatalf("content = %q", got)
	}
}

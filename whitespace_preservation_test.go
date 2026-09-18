package mekugi

import (
	"strconv"
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/patchtest"
)

func TestMutationsPreserveAuthoredWhitespace(t *testing.T) {
	for _, test := range []struct {
		name   string
		path   string
		before string
		script string
		want   string
	}{
		{
			name: "Markdown hard break", path: "file.md", before: "old\nnext\n",
			script: "type " + row(1, "old") + " " + strconv.Quote("new  "),
			want:   "new  \nnext\n",
		},
		{
			name: "mixed indentation", path: "file.txt", before: "old\n",
			script: "type " + row(1, "old") + " " + strconv.Quote("  \tnew \t "),
			want:   "  \tnew \t \n",
		},
		{
			name: "interior and EOF blank lines", path: "file.txt", before: "alpha\n",
			script: "append " + strconv.Quote("\nbeta\n \t\n\n"),
			want:   "alpha\n\nbeta\n \t\n\n",
		},
		{
			name: "inline deletion retains spaces", path: "file.txt", before: "A  X\nuntouched  \n",
			script: `type "X" ""`,
			want:   "A  \nuntouched  \n",
		},
		{
			name: "CRLF replacement", path: "file.txt", before: "old\r\nkeep\r\n",
			script: "type " + row(1, "old") + " " + strconv.Quote("new \t "),
			want:   "new \t \r\nkeep\r\n",
		},
		{
			name: "JavaScript template", path: "file.js", before: "const text = `old`;\n",
			script: "type \"old\" " + strconv.Quote("line  \n  \t\n"),
			want:   "const text = `line  \n  \t\n`;\n",
		},
		{
			name: "shell heredoc", path: "file.sh", before: "cat <<'DATA'\nold\nDATA\n",
			script: "type \"old\" " + strconv.Quote("line  \n  \t"),
			want:   "cat <<'DATA'\nline  \n  \t\nDATA\n",
		},
		{
			name: "Go raw string and formatting", path: "file.go", before: "package p\n\nvar text = `old`\n",
			script: "type \"var text = `old`\" " + strconv.Quote("var text=`line  \n \t\n`"),
			want:   "package p\n\nvar text = `line  \n \t\n`\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, root, test.path, test.before, 0o644)
			edits := []FileEdit{{Path: test.path, Script: test.script}}
			translation, err := translateForHostAtTest(t, root, edits, "")
			if err != nil {
				t.Fatalf("translate: %v, %s", err, translation.Diagnostic)
			}
			// The patch test harness matches LF lines. Verify translated text
			// here, then verify exact terminators through Apply below.
			normalized := strings.ReplaceAll(test.before, "\r\n", "\n")
			wantTranslated := strings.ReplaceAll(test.want, "\r\n", "\n")
			tree, err := patchtest.Apply(map[string]string{test.path: normalized}, string(translation.Patch))
			if err != nil || tree[test.path] != wantTranslated {
				t.Fatalf("translated content = %q, error %v, want %q", tree[test.path], err, wantTranslated)
			}
			result, err := applyForHostAtTest(t, root, edits, "")
			if err != nil {
				t.Fatalf("apply: %v, %s", err, result.Diagnostic)
			}
			if got := readTestFile(t, root, test.path); got != test.want {
				t.Fatalf("content = %q, want %q", got, test.want)
			}
		})
	}
}

func TestNewFilePreservesWhitespace(t *testing.T) {
	for _, content := range []string{"line  \n\n \t\n", " \t", "\n\n", "binary\x00payload  \n"} {
		t.Run(strconv.Quote(content), func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, root, "file.txt", "", 0o644)
			result, err := applyForHostAtTest(t, root, []FileEdit{{Path: "file.txt", Script: "append " + strings.ReplaceAll(strconv.Quote(content), `\x00`, `\u0000`)}}, "")
			if err != nil {
				t.Fatalf("apply: %v, %s", err, result.Diagnostic)
			}
			if got := readTestFile(t, root, "file.txt"); got != content {
				t.Fatalf("content = %q, want %q", got, content)
			}
		})
	}
}
func TestWhitespaceOnlyChangeHasFinalReferences(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "file.txt", "alpha\n", 0o644)
	result, err := applyForHostAtTest(t, root, []FileEdit{{Path: "file.txt", Script: "type " + row(1, "alpha") + " " + strconv.Quote("alpha \t")}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := readTestFile(t, root, "file.txt"); got != "alpha \t\n" {
		t.Fatalf("whitespace-only change lost: %q", got)
	}
	if !strings.Contains(result.Report, "files add=0 update=1 move=0 delete=0") ||
		!strings.Contains(result.Report, "1:"+hashLine("alpha \t")) {
		t.Fatalf("report does not describe the authored final state: %s", result.Report)
	}
}

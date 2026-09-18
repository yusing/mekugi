package mekugi

import (
	"os"
	"strings"
	"testing"
)

func TestSupportedLanguageSyntaxDiagnostics(t *testing.T) {
	tests := []struct {
		name        string
		path        string
		source      string
		oldLine     string
		replacement string
		language    string
		line        int
		column      int
	}{
		{
			name:        "python",
			path:        "file.py",
			source:      "def f():\n    value = 1\n",
			oldLine:     "    value = 1",
			replacement: "    value =\n",
			language:    "Python",
			line:        2,
			column:      5,
		},
		{
			name:        "javascript",
			path:        "file.js",
			source:      "function f() {\n  const value = 1;\n}\n",
			oldLine:     "  const value = 1;",
			replacement: "  const value = ;\n",
			language:    "JavaScript",
			line:        2,
			column:      15,
		},
		{
			name:        "typescript",
			path:        "file.ts",
			source:      "function f(): void {\n  const value: number = 1;\n}\n",
			oldLine:     "  const value: number = 1;",
			replacement: "  const value: number = ;\n",
			language:    "TypeScript",
			line:        2,
			column:      23,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rootPath := t.TempDir()
			writeTestFile(t, rootPath, test.path, test.source, 0o644)
			edits := []FileEdit{{Path: test.path, Script: "type " + row(2, test.oldLine) + " " + quoteTestValue(test.replacement)}}
			root, err := os.OpenRoot(rootPath)
			if err != nil {
				t.Fatal(err)
			}
			result, err := translateForHostForTest(t.Context(), Workspace{Root: root}, edits, t.TempDir())
			root.Close()
			if err == nil {
				t.Fatal("invalid source unexpectedly translated")
			}
			if len(result.Rejections) != 1 {
				t.Fatalf("rejections = %#v, want one rejection", result.Rejections)
			}
			rejection := result.Rejections[0]
			if rejection.Command != 1 || rejection.SourceLine != 1 ||
				rejection.Operation != "type" || rejection.Path != test.path ||
				rejection.Reason != "language-syntax" ||
				rejection.GeneratedLine != test.line ||
				rejection.GeneratedColumn != test.column {
				t.Fatalf("rejection = %#v", rejection)
			}
			for _, fragment := range []string{
				"parse " + test.language + " source",
				"generated " + test.language + " near ",
			} {
				if !strings.Contains(result.Diagnostic, fragment) {
					t.Fatalf("diagnostic %q lacks %q", result.Diagnostic, fragment)
				}
			}
			if got := readTestFile(t, rootPath, test.path); got != test.source {
				t.Fatalf("file = %q, want unchanged source", got)
			}
		})
	}
}

func TestLanguageSyntaxDiagnosticsCollectDistinctCommandsAndFiles(t *testing.T) {
	rootPath := t.TempDir()
	javaScript := "const first = 1;\nconst second = 2;\n"
	typeScript := "const third: number = 3;\n"
	writeTestFile(t, rootPath, "first.js", javaScript, 0o644)
	writeTestFile(t, rootPath, "second.ts", typeScript, 0o644)
	edits := []FileEdit{
		{Path: "first.js", Script: "type " + row(1, "const first = 1;") + ` "const first = ;"` + "\ntype " + row(2, "const second = 2;") + ` "const second = ;"`},
		{Path: "second.ts", Script: "type " + row(1, "const third: number = 3;") + ` "const third: number = ;"`},
	}

	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	result, err := translateForHostForTest(t.Context(), Workspace{Root: root}, edits, t.TempDir())
	root.Close()
	if err == nil {
		t.Fatal("invalid sources unexpectedly translated")
	}
	if got := result.Rejections; len(got) != 3 ||
		got[0].Command != 1 || got[0].Path != "first.js" ||
		got[1].Command != 2 || got[1].Path != "first.js" ||
		got[2].Command != 3 || got[2].Path != "second.ts" {
		t.Fatalf("rejections = %#v, want all three syntax failures", got)
	}
	if got := readTestFile(t, rootPath, "first.js"); got != javaScript {
		t.Fatalf("first.js = %q, want unchanged", got)
	}
	if got := readTestFile(t, rootPath, "second.ts"); got != typeScript {
		t.Fatalf("second.ts = %q, want unchanged", got)
	}
}

func TestLanguageCascadeCollapsePreservesSameLineColumns(t *testing.T) {
	failures := []languageSyntaxFailure{
		{line: 1, column: 7, kind: "first"},
		{line: 1, column: 20, kind: "second"},
	}
	got := collapseLanguageSyntaxCascades(
		t.Context(),
		"const first = ; const second = ;\n",
		indentationLanguageJavaScript,
		failures,
	)
	if len(got) != 2 || got[0].column != 7 || got[1].column != 20 {
		t.Fatalf("collapsed failures = %#v, want both same-line columns", got)
	}
}

func TestLanguageSyntaxDiagnosticsCollectHeredocLocationsAndCascades(t *testing.T) {
	body := "const first = ;\n" +
		strings.Repeat("const filler = 1;\n", 50) +
		"const second = ;\n"

	rootPath := t.TempDir()
	writeTestFile(t, rootPath, "file.js", "seed\n", 0o644)
	edits := []FileEdit{{Path: "file.js", Script: "type " + row(1, "seed") + " <<PATCH\n" + body + "PATCH\n"}}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	result, err := translateForHostForTest(t.Context(), Workspace{Root: root}, edits, t.TempDir())
	root.Close()
	if err == nil {
		t.Fatal("invalid JavaScript unexpectedly translated")
	}
	if got := result.Rejections; len(got) != 2 ||
		got[0].Command != 1 || got[0].SourceLine != 1 || got[0].ValueLine != 1 ||
		got[1].Command != 1 || got[1].SourceLine != 1 || got[1].ValueLine != 52 {
		t.Fatalf("rejections = %#v, want heredoc value rows 1 and 52", got)
	}
	if count := strings.Count(result.Diagnostic, `type: command 1, path "file.js", reason language-syntax: 2 distinct syntax failures`); count != 1 {
		t.Fatalf("diagnostic command groups = %d, want 1:\n%s", count, result.Diagnostic)
	}
}

func TestLanguageSyntaxDiagnosticRepairsMultilineValue(t *testing.T) {
	rootPath := t.TempDir()
	source := "def f():\n    value = 1\n"
	writeTestFile(t, rootPath, "file.py", source, 0o644)
	edits := []FileEdit{{Path: "file.py", Script: "type " + row(2, "    value = 1") + " <<PATCH\n    value =\nPATCH\n"}}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	result, err := translateForHostForTest(t.Context(), Workspace{Root: root}, edits, t.TempDir())
	root.Close()
	if err == nil {
		t.Fatal("invalid source unexpectedly translated")
	}
	if len(result.Rejections) != 1 || result.Rejections[0].Command != 1 ||
		result.Rejections[0].SourceLine != 1 ||
		result.Rejections[0].GeneratedLine != 2 || result.Rejections[0].GeneratedColumn != 5 ||
		result.Rejections[0].ValueLine != 1 {
		t.Fatalf("rejections = %#v", result.Rejections)
	}
	for _, fragment := range []string{
		"generated Python near 2:5",
		"command 1 multiline value near row 1",
		`> value row 1 | \x20\x20\x20\x20value =`,
	} {
		if !strings.Contains(result.Diagnostic, fragment) {
			t.Fatalf("diagnostic %q lacks %q", result.Diagnostic, fragment)
		}
	}
	if got := readTestFile(t, rootPath, "file.py"); got != source {
		t.Fatalf("file = %q, want unchanged source", got)
	}
}

func TestLanguageSyntaxDiagnosticAttributesUnterminatedEdit(t *testing.T) {
	rootPath := t.TempDir()
	source := "def f():\n    first = 1\n    second = 2\n"
	writeTestFile(t, rootPath, "file.py", source, 0o644)
	edits := []FileEdit{{Path: "file.py", Script: "type " + row(2, "    first = 1") + " " + quoteTestValue("    first = 3\n") + "\ntype " + row(3, "    second = 2") + " " + quoteTestValue("    second = (\n")}}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	result, err := translateForHostForTest(t.Context(), Workspace{Root: root}, edits, t.TempDir())
	root.Close()
	if err == nil {
		t.Fatal("invalid source unexpectedly translated")
	}
	if len(result.Rejections) != 1 || result.Rejections[0].Command != 2 ||
		result.Rejections[0].SourceLine != 2 ||
		result.Rejections[0].GeneratedLine < 1 || result.Rejections[0].GeneratedColumn < 1 {
		t.Fatalf("rejections = %#v, want command 2 with a generated position", result.Rejections)
	}
}

func TestUnchangedInvalidSupportedLanguageIsNotValidated(t *testing.T) {
	rootPath := t.TempDir()
	source := "def f(:\n    pass\n"
	writeTestFile(t, rootPath, "file.py", source, 0o644)
	result, err := applyForHostAtTest(t, rootPath, []FileEdit{{Path: "file.py", Script: ""}}, "")
	if err != nil || !strings.Contains(result.Report, "last none") || strings.Contains(result.Report, "language-syntax") {
		t.Fatalf("ApplyForHost() error = %v, report %q", err, result.Report)
	}
	if got := readTestFile(t, rootPath, "file.py"); got != source {
		t.Fatalf("file = %q, want unchanged source", got)
	}
}

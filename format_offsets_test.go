package mekugi

import (
	"go/format"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/patchtest"
)

func TestFormatterNormalizedLiterals(t *testing.T) {
	for _, test := range []struct{ before, after string }{
		{"0XFF", "0xFF"},
		{"0O755", "0o755"},
		{"0B101", "0b101"},
		{"1.2E3", "1.2e3"},
		{"0X1.FP+2", "0x1.Fp+2"},
		{"0x1.FP+2", "0x1.Fp+2"},
		{"000_765i", "765i"},
		{"000i", "0i"},
		{"0XFFi", "0xFFi"},
	} {
		t.Run(test.before, func(t *testing.T) {
			root := t.TempDir()
			source := "package p\nvar x = " + test.before + "\n"
			want := "package p\n\nvar x = " + test.after + "\n"
			writeTestFile(t, root, "number.go", "", 0o644)
			edits := []FileEdit{{Path: "number.go", Script: "append " + strconv.Quote(source)}}
			translated, err := TranslateForHostAt(t.Context(), root, edits, "")
			if err != nil {
				t.Fatal(err)
			}
			files, err := patchtest.Apply(map[string]string{filepath.Join(root, "number.go"): ""}, string(translated.Patch))
			if err != nil || files[filepath.Join(root, "number.go")] != want {
				t.Fatalf("translated files = %v, error = %v, want %q", files, err, want)
			}
			capability, err := os.OpenRoot(root)
			if err != nil {
				t.Fatal(err)
			}
			defer capability.Close()
			if err := Apply(t.Context(), Workspace{Root: capability}, edits); err != nil {
				t.Fatal(err)
			}
			if got := readTestFile(t, root, "number.go"); got != want {
				t.Fatalf("applied content = %q, want %q", got, want)
			}

			// Repeated canonical spellings must retain occurrence identity. A shorter
			// imaginary literal must not move the replacement endpoint to the next row.
			before := "package p\n\nvar first = " + test.after + "\nvar target = 1\nvar last = " + test.after + "\n"
			writeTestFile(t, root, "number.go", before, 0o644)
			replacement := "var target = " + test.before
			edits = []FileEdit{{Path: "number.go", Script: "type " + row(4, "var target = 1") + " " + strconv.Quote(replacement)}}
			translated, err = TranslateForHostAt(t.Context(), root, edits, "")
			if err != nil {
				t.Fatal(err)
			}
			wantRow := row(4, "var target = "+test.after)
			if len(translated.TargetAliases) != 1 || translated.TargetAliases[0].After != wantRow {
				t.Fatalf("aliases = %+v, want final target %s", translated.TargetAliases, wantRow)
			}
			if !strings.Contains(translated.Report, wantRow+" var target = "+test.after+"\n") {
				t.Fatalf("report lacks formatted target row: %s", translated.Report)
			}
		})
	}
}

func TestFormatterStructuralCorrespondence(t *testing.T) {
	for _, test := range []struct {
		name, source, target, formattedTarget string
	}{
		{"doc heading", "package p\n\n// F documentation.\n//\n// Section\n//\n// Details.\nfunc F() {}\n", "// Section", "// # Section"},
		{"doc list", "package p\n\n// F describes things:\n//\n//  - alpha\n//  - beta\nfunc F() {}\n", "//  - beta", "//   - beta"},
		{"build constraint", "// +build linux\n\npackage p\n\nvar x=1\n", "var x=1", "var x = 1"},
		{"sorted imports", "package p\nimport (\n z \"z\" // Zed\n a \"a\" // Aye\n)\n", " z \"z\" // Zed", "\tz \"z\" // Zed"},
		{"duplicate imports", "package p\nimport (\n \"z\"\n \"a\"\n \"z\"\n)\n", " \"z\"", "\t\"z\""},
		{"duplicate import spelling", "package p\nimport (\n \"z\"\n `z`\n)\n", " `z`", "\t\"z\""},
		{"duplicate import groups", "package p\nimport (\n \"z\"\n \"z\" // first\n\n \"z\" // second\n)\n", " \"z\" // first", "\t\"z\" // first"},
		{"duplicate commented imports", "package p\nimport (\n \"z\"\n \"z\" // first\n \"z\" // second\n)\n", " \"z\" // first", "\t\"z\" // first"},
		{"leading block comment z", "package p\nimport (\n\t/* Z */ \"z\"\n\t/* A */ \"a\"\n)\n", "\t/* Z */ \"z\"", "\t/* Z */ \"z\""},
		{"leading block comment a", "package p\nimport (\n\t/* Z */ \"z\"\n\t/* A */ \"a\"\n)\n", "\t/* A */ \"a\"", "\t/* A */ \"a\""},
		{"import run doc comment", "package p\nimport (\n// Z docs\n\t\"z\"\n\t\"a\"\n)\n", "// Z docs", "\t// Z docs"},
		{"empty result list", "package p\nvar f = func() () {}\n", "var f = func() () {}", "var f = func() {}"},
		{"raw carriage returns", "package p\nvar s = `a\rb`\nvar x=0XFF\n", "var x=0XFF", "var x = 0xFF"},
		{"raw import carriage return", "package p\nimport (\n `z\r`\n \"a\"\n)\n", " \"a\"", "\t\"a\""},
	} {
		t.Run(test.name, func(t *testing.T) {
			formatted, err := format.Source([]byte(test.source))
			if err != nil {
				t.Fatal(err)
			}
			want := string(formatted)
			if !strings.Contains(want, test.formattedTarget) {
				t.Fatalf("formatter result %q lacks expected target %q", want, test.formattedTarget)
			}
			mapping, err := newFormattedOffsetMap(test.source, want)
			if err != nil {
				t.Fatal(err)
			}
			start := strings.LastIndex(test.source, test.target)
			end := start + len(test.target)
			wantLine := strings.Count(want[:strings.LastIndex(want, test.formattedTarget)], "\n") + 1
			for _, endpoint := range []int{start, end} {
				mapped := mapping.mapOffset(endpoint)
				if line := strings.Count(want[:mapped], "\n") + 1; line != wantLine {
					t.Fatalf("endpoint %d maps to line %d, want %d; formatted %q", endpoint, line, wantLine, want)
				}
			}
			root := t.TempDir()
			writeTestFile(t, root, "formatted.go", "", 0o644)
			edits := []FileEdit{{Path: "formatted.go", Script: "append " + strconv.Quote(test.source)}}
			translated, err := TranslateForHostAt(t.Context(), root, edits, "")
			if err != nil {
				t.Fatal(err)
			}
			files, err := patchtest.Apply(map[string]string{filepath.Join(root, "formatted.go"): ""}, string(translated.Patch))
			if err != nil || files[filepath.Join(root, "formatted.go")] != want {
				t.Fatalf("translated files = %v, error = %v, want %q", files, err, want)
			}
			applied, err := ApplyForHostAt(t.Context(), root, edits, "")
			if err != nil {
				t.Fatal(err)
			}
			if got := readTestFile(t, root, "formatted.go"); got != want || applied.Report != translated.Report {
				t.Fatalf("apply/translate mismatch: content %q, apply report %q, translate report %q", got, applied.Report, translated.Report)
			}
			// Exercise row aliases on an effective edit, including a duplicate
			// import whose authored occurrence disappears during formatting.
			baseline := test.source[:end] + " " + test.source[end:]
			writeTestFile(t, root, "formatted.go", baseline, 0o644)
			oldRow := row(strings.Count(test.source[:start], "\n")+1, test.target+" ")
			edits = []FileEdit{{Path: "formatted.go", Script: "type " + oldRow + " " + strconv.Quote(test.target)}}
			translated, err = TranslateForHostAt(t.Context(), root, edits, "")
			if err != nil {
				t.Fatal(err)
			}
			wantRow := row(wantLine, test.formattedTarget)
			if len(translated.TargetAliases) != 1 || translated.TargetAliases[0].After != wantRow {
				t.Fatalf("aliases = %+v, want %s", translated.TargetAliases, wantRow)
			}
		})
	}
}

func TestFormattedOffsetsRejectChangedCode(t *testing.T) {
	if _, err := newFormattedOffsetMap("package p\nvar x = 1\n", "package p\nvar x = 2\n"); err == nil {
		t.Fatal("changed literal unexpectedly has formatter correspondence")
	}
}

func TestFormatterReorderedImportRangeAliases(t *testing.T) {
	for _, paths := range [][]string{{"z", "a"}, {"b", "z", "a"}, {"b", "a", "z"}} {
		t.Run(strings.Join(paths, "_"), func(t *testing.T) {
			root := t.TempDir()
			var before, replacement strings.Builder
			before.WriteString("package p\n\nimport (\n")
			for i, path := range paths {
				before.WriteString("\t" + strconv.Quote("old"+path) + "\n")
				if i > 0 {
					replacement.WriteByte('\n')
				}
				replacement.WriteString("\t" + strconv.Quote(path))
			}
			before.WriteString(")\n\nvar x = 1\n")
			writeTestFile(t, root, "imports.go", before.String(), 0o644)
			target := row(4, "\t"+strconv.Quote("old"+paths[0])) + ".." + row(3+len(paths), "\t"+strconv.Quote("old"+paths[len(paths)-1]))
			edits := []FileEdit{{Path: "imports.go", Script: "type " + target + " " + strconv.Quote(replacement.String())}}
			translated, err := TranslateForHostAt(t.Context(), root, edits, "")
			if err != nil {
				t.Fatal(err)
			}
			want := row(4, "\t\"a\"") + ".." + row(3+len(paths), "\t\"z\"")
			if len(translated.TargetAliases) != 1 || translated.TargetAliases[0].After != want {
				t.Fatalf("aliases = %+v, want %s", translated.TargetAliases, want)
			}
			applied, err := ApplyForHostAt(t.Context(), root, edits, "")
			if err != nil || applied.Report != translated.Report {
				t.Fatalf("apply error %v, apply report %q, translate report %q", err, applied.Report, translated.Report)
			}
			// Report endpoints retain the exclusive boundary row, unlike the
			// inclusive replacement alias, which ends on the last import row.
			for _, expected := range []string{row(4, "\t\"a\"") + " \\t\"a\"\n", row(4+len(paths), ")") + " )\n"} {
				if !strings.Contains(translated.Report, expected) {
					t.Fatalf("report %q lacks endpoint %q", translated.Report, expected)
				}
			}
		})
	}
}

func TestFormatterImportDeclarationCommentBoundaries(t *testing.T) {
	for _, source := range []string{
		"package p\n/* C */ import \"z\"\n",
		"package p\n/* C */ import (\n\t\"z\"\n\t\"a\"\n)\n",
		"package p\nimport /* C */ \"z\"\n",
		"package p\nimport ( /* C */ \"z\"\n\t\"a\"\n)\n",
	} {
		t.Run(source, func(t *testing.T) {
			root := t.TempDir()
			formatted, err := format.Source([]byte(source))
			if err != nil {
				t.Fatal(err)
			}
			writeTestFile(t, root, "imports.go", "", 0o644)
			edits := []FileEdit{{Path: "imports.go", Script: "append " + strconv.Quote(source)}}
			translated, err := TranslateForHostAt(t.Context(), root, edits, "")
			if err != nil {
				t.Fatal(err)
			}
			files, err := patchtest.Apply(map[string]string{filepath.Join(root, "imports.go"): ""}, string(translated.Patch))
			if err != nil || files[filepath.Join(root, "imports.go")] != string(formatted) {
				t.Fatalf("translated files %v, error %v, want %q", files, err, formatted)
			}
			capability, err := os.OpenRoot(root)
			if err != nil {
				t.Fatal(err)
			}
			defer capability.Close()
			if err := Apply(t.Context(), Workspace{Root: capability}, edits); err != nil {
				t.Fatal(err)
			}
			if got := readTestFile(t, root, "imports.go"); got != string(formatted) {
				t.Fatalf("applied content %q, want %q", got, formatted)
			}
		})
	}
}

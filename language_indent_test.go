package mekugi

import (
	"strconv"
	"strings"
	"testing"
)

func TestPythonWrapperIndentationCorrection(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "file.py", "def f():\n    if ready:\n        existing()\n    return\n", 0o644)
	command := "type " + row(3, "        existing()") + " " + quoteTestValue("        if ready:\n        existing()\n")
	result, err := applyForHostAtTest(t, root, []FileEdit{{Path: "file.py", Script: command}}, "")
	if err != nil {
		t.Fatalf("ApplyForHost() error = %v, diagnostic %q", err, result.Diagnostic)
	}
	want := "def f():\n    if ready:\n        if ready:\n            existing()\n    return\n"
	if got := readTestFile(t, root, "file.py"); got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
}

func TestPythonWrapperRightShiftAndCorrectChild(t *testing.T) {
	for _, proposed := range []string{"                existing()", "            existing()"} {
		t.Run(strings.TrimSpace(proposed), func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, root, "file.py", "def f():\n    if ready:\n        existing()\n    return\n", 0o644)
			command := "type " + row(3, "        existing()") + " " + quoteTestValue("        if ready:\n"+proposed+"\n")
			result, err := applyForHostAtTest(t, root, []FileEdit{{Path: "file.py", Script: command}}, "")
			if err != nil {
				t.Fatalf("ApplyForHost() error = %v, diagnostic %q", err, result.Diagnostic)
			}
			want := "def f():\n    if ready:\n        if ready:\n            existing()\n    return\n"
			if got := readTestFile(t, root, "file.py"); got != want {
				t.Fatalf("content = %q, want %q", got, want)
			}
		})
	}
}

func TestJavaScriptWrapperIndentationCorrection(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "file.js", "function f() {\n  if (ready) {\n    existing();\n  }\n}\n", 0o644)
	command := "type " + row(3, "    existing();") + " " + quoteTestValue("    if (ready) {\n    existing();\n    }\n")
	result, err := applyForHostAtTest(t, root, []FileEdit{{Path: "file.js", Script: command}}, "")
	if err != nil {
		t.Fatalf("ApplyForHost() error = %v, diagnostic %q", err, result.Diagnostic)
	}
	want := "function f() {\n  if (ready) {\n    if (ready) {\n      existing();\n    }\n  }\n}\n"
	if got := readTestFile(t, root, "file.js"); got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
}

func TestTypeScriptWrapperIndentationCorrection(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "file.ts", "function f(): void {\n  if (ready) {\n    existing();\n  }\n}\n", 0o644)
	command := "type " + row(3, "    existing();") + " " + quoteTestValue("    if (ready) {\n    existing();\n    }\n")
	result, err := applyForHostAtTest(t, root, []FileEdit{{Path: "file.ts", Script: command}}, "")
	if err != nil {
		t.Fatalf("ApplyForHost() error = %v, diagnostic %q", err, result.Diagnostic)
	}
	want := "function f(): void {\n  if (ready) {\n    if (ready) {\n      existing();\n    }\n  }\n}\n"
	if got := readTestFile(t, root, "file.ts"); got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
}

func TestJavaScriptAndTypeScriptWrapperRightShiftAndCorrectChild(t *testing.T) {
	tests := []struct {
		name        string
		path        string
		source      string
		replacement string
	}{
		{
			name:        "js right-shifted",
			path:        "file.js",
			source:      "function f() {\n  if (ready) {\n    existing();\n  }\n}\n",
			replacement: "    if (ready) {\n        existing();\n    }\n",
		},
		{
			name:        "js already-correct",
			path:        "file.js",
			source:      "function f() {\n  if (ready) {\n    existing();\n  }\n}\n",
			replacement: "    if (ready) {\n      existing();\n    }\n",
		},
		{
			name:        "ts right-shifted",
			path:        "file.ts",
			source:      "function f(): void {\n  if (ready) {\n    existing();\n  }\n}\n",
			replacement: "    if (ready) {\n        existing();\n    }\n",
		},
		{
			name:        "ts already-correct",
			path:        "file.ts",
			source:      "function f(): void {\n  if (ready) {\n    existing();\n  }\n}\n",
			replacement: "    if (ready) {\n      existing();\n    }\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, root, test.path, test.source, 0o644)
			command := "type " + row(3, "    existing();") + " " + quoteTestValue(test.replacement)
			result, err := applyForHostAtTest(t, root, []FileEdit{{Path: test.path, Script: command}}, "")
			if err != nil {
				t.Fatalf("ApplyForHost() error = %v, diagnostic %q", err, result.Diagnostic)
			}
			wantReplacement := "    if (ready) {\n      existing();\n    }\n"
			want := strings.Replace(test.source, "    existing();\n", wantReplacement, 1)
			if got := readTestFile(t, root, test.path); got != want {
				t.Fatalf("content = %q, want %q", got, want)
			}
		})
	}
}

func TestSupportedExactIndentationCorrection(t *testing.T) {
	for _, extension := range []string{"py", "js", "ts"} {
		t.Run(extension, func(t *testing.T) {
			root := t.TempDir()
			path := "file." + extension
			writeTestFile(t, root, path, "header\n    value\n", 0o644)
			command := "type " + row(2, "    value") + " " + quoteTestValue("value\n")
			result, err := applyForHostAtTest(t, root, []FileEdit{{Path: path, Script: command}}, "")
			if err != nil {
				t.Fatalf("ApplyForHost() error = %v, diagnostic %q", err, result.Diagnostic)
			}
			if got := readTestFile(t, root, path); got != "header\n    value\n" {
				t.Fatalf("content = %q", got)
			}
		})
	}
}

func TestUnknownWrapperDoesNotReject(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "file.txt", "    existing()\n", 0o644)
	command := "type " + row(1, "    existing()") + " " + quoteTestValue("    if (ready) {\n    existing()\n    }\n")
	result, err := applyForHostAtTest(t, root, []FileEdit{{Path: "file.txt", Script: command}}, "")
	if err != nil {
		t.Fatalf("ApplyForHost() error = %v, diagnostic %q", err, result.Diagnostic)
	}
	if got := readTestFile(t, root, "file.txt"); !strings.Contains(got, "if (ready)") {
		t.Fatalf("content = %q", got)
	}
}

func TestPreservedCommentIsNotWrapperAutofixed(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "file.py", "def f():\n    if ready:\n        # comment\n", 0o644)
	replacement := "        if nested:\n        # comment\n"
	command := "type " + row(3, "        # comment") + " " + quoteTestValue(replacement)
	result, err := applyForHostAtTest(t, root, []FileEdit{{Path: "file.py", Script: command}}, "")
	if err != nil {
		t.Fatalf("ApplyForHost() error = %v, diagnostic %q", err, result.Diagnostic)
	}
	want := "def f():\n    if ready:\n" + replacement
	if got := readTestFile(t, root, "file.py"); got != want {
		t.Fatalf("content = %q, want submitted comment indentation", got)
	}
}

func quoteTestValue(value string) string {
	return strconv.Quote(value)
}

func TestMultipleSupportedExactCandidatesApply(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "file.py", "header\n    first\nmiddle\n    second\n", 0o644)
	first := "type " + row(2, "    first") + " " + quoteTestValue("first\n")
	second := "type " + row(4, "    second") + " " + quoteTestValue("second\n")
	result, err := applyForHostAtTest(t, root, []FileEdit{{Path: "file.py", Script: first + "\n" + second}}, "")
	if err != nil {
		t.Fatalf("ApplyForHost() error = %v, diagnostic %q", err, result.Diagnostic)
	}
	if got := readTestFile(t, root, "file.py"); got != "header\n    first\nmiddle\n    second\n" {
		t.Fatalf("content = %q", got)
	}
}

func TestWrapperWithoutUnambiguousUnitRemainsByteExact(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "file.py", "value()\n", 0o644)
	command := "type " + row(1, "value()") + " " + quoteTestValue("if ready:\nvalue()\n")
	result, err := applyForHostAtTest(t, root, []FileEdit{{Path: "file.py", Script: command}}, "")
	if err != nil {
		t.Fatalf("ApplyForHost() error = %v, diagnostic %q", err, result.Diagnostic)
	}
	want := "if ready:\nvalue()\n"
	if got := readTestFile(t, root, "file.py"); got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
}

func TestPythonWrapperShapeRejectionsRemainSubmitted(t *testing.T) {
	tests := []string{
		"        if ready: pass\n            value()\n",
		"        if ready: # comment\n            value()\n",
		"        if ready:\n            other()\n",
	}
	for _, replacement := range tests {
		t.Run(strings.TrimSpace(replacement), func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, root, "file.py", "def f():\n    if ready:\n        value()\n", 0o644)
			command := "type " + row(3, "        value()") + " " + quoteTestValue(replacement)
			result, err := applyForHostAtTest(t, root, []FileEdit{{Path: "file.py", Script: command}}, "")
			if err != nil {
				t.Fatalf("ApplyForHost() error = %v, diagnostic %q", err, result.Diagnostic)
			}
			if got := readTestFile(t, root, "file.py"); got != "def f():\n    if ready:\n"+replacement {
				t.Fatalf("content = %q, want %q", got, replacement)
			}
		})
	}
}

func TestWrapperUnrelatedParseErrorIsRejectedAtomically(t *testing.T) {
	root := t.TempDir()
	before := "def f():\n    if ready:\n        value()\n    broken = (\n"
	writeTestFile(t, root, "file.py", before, 0o644)
	replacement := "        if ready:\n        value()\n"
	command := "type " + row(3, "        value()") + " " + quoteTestValue(replacement)
	result, err := applyForHostAtTest(t, root, []FileEdit{{Path: "file.py", Script: command}}, "")
	if err == nil || !strings.Contains(result.Diagnostic, "language-syntax") ||
		!strings.Contains(result.Diagnostic, "parse Python source") {
		t.Fatalf("ApplyForHost() error = %v, diagnostic %q", err, result.Diagnostic)
	}
	if got := readTestFile(t, root, "file.py"); got != before {
		t.Fatalf("content = %q, want unchanged", got)
	}
}

func TestMixedStructuralUnitsRemainByteExact(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "file.js", "function f() {\n  if (a) {\n    first();\n  }\n    if (b) {\n       second();\n    }\n}\n", 0o644)
	replacement := "    if (ready) {\n    first();\n    }\n"
	command := "type " + row(3, "    first();") + " " + quoteTestValue(replacement)
	result, err := applyForHostAtTest(t, root, []FileEdit{{Path: "file.js", Script: command}}, "")
	if err != nil {
		t.Fatalf("ApplyForHost() error = %v, diagnostic %q", err, result.Diagnostic)
	}
	want := "function f() {\n  if (a) {\n" + replacement + "  }\n    if (b) {\n       second();\n    }\n}\n"
	if got := readTestFile(t, root, "file.js"); got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
}

func TestPythonWrapperTabIndentationUnit(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "file.py", "def f():\n\tif ready:\n\t\texisting()\n", 0o644)
	replacement := "\t\tif ready:\n\texisting()\n"
	command := "type " + row(3, "\t\texisting()") + " " + quoteTestValue(replacement)
	result, err := applyForHostAtTest(t, root, []FileEdit{{Path: "file.py", Script: command}}, "")
	if err != nil {
		t.Fatalf("ApplyForHost() error = %v, diagnostic %q", err, result.Diagnostic)
	}
	want := "def f():\n\tif ready:\n\t\tif ready:\n\t\t\texisting()\n"
	if got := readTestFile(t, root, "file.py"); got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
}

func TestWrapperCorrectionFinalReferencesUseCorrectedContent(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "file.py", "def f():\n    if ready:\n        existing()\n    return\n", 0o644)
	replacement := "        if ready:\n        existing()\n"
	command := "type " + row(3, "        existing()") + " " + quoteTestValue(replacement)
	result, err := applyForHostAtTest(t, root, []FileEdit{{Path: "file.py", Script: command}}, "")
	if err != nil {
		t.Fatalf("ApplyForHost() error = %v, report %q", err, result.Report)
	}
	if !strings.Contains(result.Report, "refs 1 type\n") ||
		!strings.Contains(result.Report, "3:"+hashLine("        if ready:")+` \x20\x20\x20\x20\x20\x20\x20\x20if ready:`+"\n") {
		t.Fatalf("report = %q", result.Report)
	}
}

func TestMultipleWrapperCandidatesUseOneFinalProbe(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "file.py", "def f():\n    first()\n    second()\n", 0o644)
	first := "type " + row(2, "    first()") + " " +
		quoteTestValue("    if first_ready:\n    first()\n")
	second := "type " + row(3, "    second()") + " " +
		quoteTestValue("    if second_ready:\n        second()\n")
	result, err := applyForHostAtTest(t, root, []FileEdit{{Path: "file.py", Script: first + "\n" + second}}, "")
	if err != nil {
		t.Fatalf("ApplyForHost() error = %v, diagnostic %q", err, result.Diagnostic)
	}
	want := "def f():\n" +
		"    if first_ready:\n" +
		"        first()\n" +
		"    if second_ready:\n" +
		"        second()\n"
	if got := readTestFile(t, root, "file.py"); got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
}

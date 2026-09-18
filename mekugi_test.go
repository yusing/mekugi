package mekugi

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/patchtest"
)

func TestMekugi2NormalMultiFileWorkflow(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "a.txt", "alpha old\nkeep\nend\n", 0o750)
	if err := os.Chmod(filepath.Join(root, "a.txt"), 0o750); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, root, "b.txt", "x x x\n", 0o640)
	writeTestFile(t, root, "obsolete.txt", "remove me\n", 0o640)

	script := strings.Join([]string{
		"in a.txt",
		"type " + row(1, "alpha old") + ` "old" "new"`,
		"add " + row(2, "keep") + ` "// note\n"`,
		"type " + row(3, "end") + ` ""`,
		"in b.txt",
		"type " + row(1, "x x x") + ` "x" 2 "y"`,
		"new draft.txt",
		`type "foo bar"`,
		"mv final.txt",
		"in obsolete.txt",
		"rm",
	}, "\n")

	result, err := applyForHostAtTest(t, root, script, "")
	if err != nil {
		t.Fatalf("ApplyForHost() error = %v, diagnostic %q", err, result.Diagnostic)
	}
	if !strings.Contains(result.Report, "files add=1 update=2 move=0 delete=1\n") {
		t.Fatalf("report = %q", result.Report)
	}
	want := map[string]string{
		"a.txt":     "alpha new\n// note\nkeep\n",
		"b.txt":     "y y x\n",
		"final.txt": "foo bar",
	}
	if got := readTree(t, root); !reflect.DeepEqual(got, want) {
		t.Fatalf("tree = %#v, want %#v", got, want)
	}
	if mode := testFileMode(t, filepath.Join(root, "a.txt")); mode != 0o750 {
		t.Fatalf("a.txt mode = %o, want 750", mode)
	}
}

func TestMekugi2TranslateMatchesNormalMode(t *testing.T) {
	initial := map[string]string{
		"code.go":     "package sample\n\nvar value = old\n",
		"obsolete.go": "package obsolete\n",
	}
	root := t.TempDir()
	for path, content := range initial {
		writeTestFile(t, root, path, content, 0o644)
	}
	script := strings.Join([]string{
		"in code.go",
		"type " + row(3, "var value = old") + ` "old" "current"`,
		"mv current.go",
		"new note.txt",
		`type "hello world\n"`,
		"in obsolete.go",
		"rm",
	}, "\n")

	result, err := translateForHostAtTest(t, root, script, "")
	if err != nil {
		t.Fatalf("translateForHostForTest() error = %v, diagnostic %q", err, result.Diagnostic)
	}
	if !strings.Contains(result.Report, "files add=1 update=1 move=1 delete=1\n") {
		t.Fatalf("report = %q", result.Report)
	}
	if got := readTree(t, root); !reflect.DeepEqual(got, initial) {
		t.Fatalf("translate mutated tree: %#v", got)
	}
	translated, err := patchtest.Apply(initial, string(result.Patch))
	if err != nil {
		t.Fatalf("applying translated patch: %v\n%s", err, string(result.Patch))
	}
	want := map[string]string{
		"current.go": "package sample\n\nvar value = current\n",
		"note.txt":   "hello world\n",
	}
	if !reflect.DeepEqual(translated, want) {
		t.Fatalf("translated tree = %#v, want %#v\n%s", translated, want, string(result.Patch))
	}
}

func TestMekugi2LineAndRangeTerminatorSemantics(t *testing.T) {
	tests := []struct {
		name, content, script, want string
	}{
		{"line preserves CRLF", "one\r\ntwo\r\n", "in file.txt\ntype " + row(1, "one") + ` "ONE"`, "ONE\r\ntwo\r\n"},
		{"range preserves final CR", "one\rtwo\rthree", "in file.txt\ntype " + row(1, "one") + ".." + row(2, "two") + ` "both"`, "both\rthree"},
		{"unterminated final replacement", "one\ntwo", "in file.txt\ntype " + row(2, "two") + ` "TWO"`, "one\nTWO"},
		{"empty line removes LF", "one\ntwo\n", "in file.txt\ntype " + row(1, "one") + ` ""`, "two\n"},
		{"empty line removes CRLF", "one\r\ntwo\r\n", "in file.txt\ntype " + row(1, "one") + ` ""`, "two\r\n"},
		{"empty line removes standalone CR", "one\rtwo\r", "in file.txt\ntype " + row(1, "one") + ` ""`, "two\r"},
		{"empty unterminated final line", "one\ntwo", "in file.txt\ntype " + row(2, "two") + ` ""`, "one\n"},
		{"empty range removes final terminator", "one\ntwo\nthree\n", "in file.txt\ntype " + row(1, "one") + ".." + row(2, "two") + ` ""`, "three\n"},
		{"empty heredoc removes line", "one\ntwo\n", "in file.txt\ntype " + row(1, "one") + " <<PATCH\nPATCH\n", "two\n"},
		{"empty text replacement keeps terminator", "one\ntwo\n", "in file.txt\ntype " + row(1, "one") + ` "one" ""`, "\ntwo\n"},
		{"explicit terminator creates blank line", "one\ntwo\n", "in file.txt\ntype " + row(1, "one") + ` "\n"`, "\ntwo\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, root, "file.txt", test.content, 0o644)
			result, err := applyForHostAtTest(t, root, test.script, "")
			if err != nil {
				t.Fatalf("ApplyForHost() error = %v, diagnostic %q", err, result.Diagnostic)
			}
			if got := readTestFile(t, root, "file.txt"); got != test.want {
				t.Fatalf("file = %q, want %q", got, test.want)
			}
		})
	}
}

func TestMekugi2EmptyInitializerRemainsEmptyFile(t *testing.T) {
	root := t.TempDir()
	result, err := applyForHostAtTest(t, root, "new empty.txt\ntype \"\"\n", "")
	if err != nil {
		t.Fatalf("ApplyForHost() error = %v, diagnostic %q", err, result.Diagnostic)
	}
	if got := readTestFile(t, root, "empty.txt"); got != "" {
		t.Fatalf("file = %q, want empty", got)
	}
}

func TestMekugi2SameBoundaryInsertionsKeepScriptOrder(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "file.txt", "target\n", 0o644)
	target := row(1, "target")
	script := strings.Join([]string{
		"in file.txt",
		`add ` + target + ` "first\n"`,
		`add ` + target + ` "second\n"`,
		`add EOF "after-one\n"`,
		`add EOF "after-two\n"`,
	}, "\n")
	result, err := applyForHostAtTest(t, root, script, "")
	if err != nil {
		t.Fatalf("ApplyForHost() error = %v, diagnostic %q", err, result.Diagnostic)
	}
	want := "first\nsecond\ntarget\nafter-one\nafter-two\n"
	if got := readTestFile(t, root, "file.txt"); got != want {
		t.Fatalf("file = %q, want %q", got, want)
	}
}

func TestMekugi2InsertionsAtReplacementBoundariesAreAllowed(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "file.txt", "target\n", 0o644)
	target := row(1, "target")
	script := strings.Join([]string{
		"in file.txt",
		`type ` + target + ` "replacement"`,
		`add ` + target + ` "before\n"`,
		`add EOF "after\n"`,
	}, "\n")
	result, err := applyForHostAtTest(t, root, script, "")
	if err != nil {
		t.Fatalf("ApplyForHost() error = %v, diagnostic %q", err, result.Diagnostic)
	}
	if got, want := readTestFile(t, root, "file.txt"), "before\nreplacement\nafter\n"; got != want {
		t.Fatalf("file = %q, want %q", got, want)
	}
}

func TestMekugi2RejectsInvalidTargetsAtomically(t *testing.T) {
	tests := []struct{ name, script, reason string }{
		{"stale", "in file.txt\ntype 2:0000 \"B\"", "row-stale"},
		{"missing", "in file.txt\ntype 9:0000 \"B\"", "row-missing"},
		{"incomplete", "in file.txt\ntype " + row(1, "alpha") + ` "alpha" 2 "A"`, "occurrence-missing"},
		{"invalid count", "in file.txt\ntype " + row(1, "alpha") + ` "alpha" 0 "A"`, "invalid-count"},
		{"reversed", "in file.txt\ntype " + row(2, "beta") + ".." + row(1, "alpha") + ` ""`, "target-order"},
		{"overlap", "in file.txt\ntype " + row(1, "alpha") + ` "A"` + "\ntype " + row(1, "alpha") + ` ""`, "edit-conflict"},
		{"insertion inside replacement", "in file.txt\ntype " + row(1, "alpha") + ` "A"` + "\nadd " + row(1, "alpha") + ` "ph" "x"`, "edit-conflict"},
		{"introduced", "in file.txt\nadd " + row(2, "beta") + ` "new\n"` + "\ntype " + row(1, "alpha") + ` "new" "NEW"`, "occurrence-missing"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, root, "file.txt", "alpha\nbeta\n", 0o644)
			before := readTestFile(t, root, "file.txt")
			result, err := applyForHostAtTest(t, root, test.script, "")
			if err == nil || !strings.Contains(result.Diagnostic, test.reason) {
				t.Fatalf("ApplyForHost() error = %v, diagnostic %q; want %q", err, result.Diagnostic, test.reason)
			}
			if got := readTestFile(t, root, "file.txt"); got != before {
				t.Fatalf("rejection changed file to %q", got)
			}
		})
	}
}

func TestMekugi2RangeVerificationChecksEndpointsNotInterior(t *testing.T) {
	script := "in file.txt\ntype " + row(1, "first") + ".." + row(3, "last") + ` "replacement"`
	for _, mode := range []string{"apply", "translate"} {
		for _, test := range []struct {
			name, current string
			reject        bool
		}{
			{"unchanged endpoints", "first\nchanged since inspection\nlast\n", false},
			{"changed start", "new first\ninterior\nlast\n", true},
			{"changed end", "first\ninterior\nnew last\n", true},
		} {
			t.Run(mode+"/"+test.name, func(t *testing.T) {
				root := t.TempDir()
				writeTestFile(t, root, "file.txt", test.current, 0o644)
				var result HostTranslation
				var err error
				if mode == "apply" {
					result, err = applyForHostAtTest(t, root, script, "")
				} else {
					result, err = TranslateForHostAt(t.Context(), root, script, "")
				}
				if test.reject {
					if err == nil || !strings.Contains(result.Diagnostic, "row-stale") || len(result.Patch) != 0 {
						t.Fatalf("changed endpoint accepted: %+v, %v", result, err)
					}
					if got := readTestFile(t, root, "file.txt"); got != test.current {
						t.Fatalf("rejection changed file to %q", got)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				got := readTestFile(t, root, "file.txt")
				if mode == "translate" {
					if got != test.current {
						t.Fatal("translation changed the workspace")
					}
					tree, err := patchtest.Apply(map[string]string{"file.txt": got}, string(result.Patch))
					if err != nil {
						t.Fatal(err)
					}
					got = tree["file.txt"]
				}
				if got != "replacement\n" {
					t.Fatalf("range result = %q", got)
				}
			})
		}
	}
}

func TestCallerCoordinatedEditsRefreshBaselineAfterHandoff(t *testing.T) {
	for _, mode := range []string{"Apply", "ApplyForHost", "ApplyForHostRoot", "TranslateForHostAt"} {
		t.Run(mode, func(t *testing.T) {
			directory := t.TempDir()
			writeTestFile(t, directory, "file.txt", "first\n", 0o644)
			root, err := os.OpenRoot(directory)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			// The caller completes each read/edit/apply cycle before handing the
			// file to the next writer. Translation alone does not finish the cycle.
			for _, replacement := range []string{"second", "third"} {
				current := readTestFile(t, directory, "file.txt")
				script := "in file.txt\ntype " + row(1, strings.TrimSuffix(current, "\n")) + " " + fmt.Sprintf("%q", replacement)
				switch mode {
				case "Apply":
					err = Apply(t.Context(), Workspace{Root: root}, script)
				case "ApplyForHost":
					_, err = ApplyForHost(t.Context(), Workspace{Root: root}, script, "")
				case "ApplyForHostRoot":
					_, err = ApplyForHostRoot(t.Context(), root, script, "")
				case "TranslateForHostAt":
					var result HostTranslation
					result, err = TranslateForHostAt(t.Context(), directory, script, "")
					if err != nil {
						break
					}
					if got := readTestFile(t, directory, "file.txt"); got != current {
						t.Fatal("translation changed the workspace")
					}
					var applied map[string]string
					applied, err = patchtest.Apply(map[string]string{"file.txt": current}, string(result.Patch))
					if err == nil {
						err = root.WriteFile("file.txt", []byte(applied["file.txt"]), 0o644)
					}
				}
				if err != nil {
					t.Fatal(err)
				}
				if got := readTestFile(t, directory, "file.txt"); got != replacement+"\n" {
					t.Fatalf("completed cycle = %q, want %q", got, replacement+"\n")
				}
			}
		})
	}
}

func TestMekugi2RelocatesUniqueRowsAfterPriorEdits(t *testing.T) {
	tests := []struct {
		name    string
		content string
		script  string
		want    string
	}{
		{
			name:    "line shifted down",
			content: "inserted\nalpha\nbeta\n",
			script:  "in file.txt\ntype " + row(1, "alpha") + ` "ALPHA"`,
			want:    "inserted\nALPHA\nbeta\n",
		},
		{
			name:    "line shifted above prior eof",
			content: "alpha\n",
			script:  "in file.txt\ntype " + row(2, "alpha") + ` "ALPHA"`,
			want:    "ALPHA\n",
		},
		{
			name:    "range endpoints shifted down",
			content: "inserted\nalpha\nbeta\ngamma\n",
			script:  "in file.txt\ntype " + row(1, "alpha") + ".." + row(2, "beta") + ` "AB"`,
			want:    "inserted\nAB\ngamma\n",
		},
		{
			name:    "text anchor shifted down",
			content: "inserted\nalpha old\nbeta\n",
			script:  "in file.txt\ntype " + row(1, "alpha old") + ` "old" "new"`,
			want:    "inserted\nalpha new\nbeta\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, root, "file.txt", test.content, 0o644)
			result, err := applyForHostAtTest(t, root, test.script, "")
			if err != nil {
				t.Fatalf("ApplyForHost() error = %v, diagnostic %q", err, result.Diagnostic)
			}
			if got := readTestFile(t, root, "file.txt"); got != test.want {
				t.Fatalf("file = %q, want %q", got, test.want)
			}
		})
	}
}

func TestMekugi2ResolvesPostEditCoordinateForUnchangedBaselineRow(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "file.txt", "alpha\n}\nbeta\n}\n", 0o644)
	script := "in file.txt\n" +
		"add " + row(1, "alpha") + ` "one\ntwo\n"` + "\n" +
		"add " + row(6, "}") + ` "tail\n"`
	result, err := applyForHostAtTest(t, root, script, "")
	if err != nil {
		t.Fatalf("ApplyForHost() error = %v, diagnostic %q", err, result.Diagnostic)
	}
	want := "one\ntwo\nalpha\n}\nbeta\ntail\n}\n"
	if got := readTestFile(t, root, "file.txt"); got != want {
		t.Fatalf("file = %q, want %q", got, want)
	}
}

func TestMekugi2DoesNotResolvePostEditCoordinateForIntroducedRow(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "file.txt", "alpha\nbeta\n", 0o644)
	script := "in file.txt\n" +
		"add " + row(2, "beta") + ` "new\n"` + "\n" +
		"type " + row(2, "new") + ` "NEW"`
	result, err := applyForHostAtTest(t, root, script, "")
	if err == nil || !strings.Contains(result.Diagnostic, "row-stale") {
		t.Fatalf("ApplyForHost() error = %v, diagnostic %q", err, result.Diagnostic)
	}
	if got := readTestFile(t, root, "file.txt"); got != "alpha\nbeta\n" {
		t.Fatalf("rejection changed file to %q", got)
	}
}

func TestMekugi2RejectsAmbiguousRelocatedRow(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "file.txt", "other\nalpha\nalpha\n", 0o644)
	before := readTestFile(t, root, "file.txt")
	script := "in file.txt\ntype " + row(1, "alpha") + ` "ALPHA"`
	result, err := applyForHostAtTest(t, root, script, "")
	if err == nil || !strings.Contains(result.Diagnostic, "row-stale") || !strings.Contains(result.Diagnostic, "ambiguous across 2 rows") {
		t.Fatalf("ApplyForHost() error = %v, diagnostic %q", err, result.Diagnostic)
	}
	if got := readTestFile(t, root, "file.txt"); got != before {
		t.Fatalf("rejection changed file to %q", got)
	}
}

func TestMekugi2IgnoresRedundantStaleLiteralAnchor(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "file.txt", "alpha\nunique target\nomega\n", 0o644)
	script := `in file.txt
type 1:ffff "unique target" "replacement"`
	result, err := applyForHostAtTest(t, root, script, "")
	if err != nil {
		t.Fatalf("ApplyForHost() error = %v, diagnostic %q", err, result.Diagnostic)
	}
	if content := readTestFile(t, root, "file.txt"); content != "alpha\nreplacement\nomega\n" {
		t.Fatalf("content = %q", content)
	}
}

func TestMekugi2RejectsStaleLiteralAnchorWhenLiteralIsAmbiguous(t *testing.T) {
	root := t.TempDir()
	before := "target\nanchor\ntarget\n"
	writeTestFile(t, root, "file.txt", before, 0o644)
	script := `in file.txt
type 2:ffff "target" "replacement"`
	result, err := applyForHostAtTest(t, root, script, "")
	if err == nil {
		t.Fatalf("ApplyForHost() unexpectedly succeeded, diagnostic %q", result.Diagnostic)
	}
	if !strings.Contains(result.Diagnostic, "reason row-stale") {
		t.Fatalf("diagnostic = %q", result.Diagnostic)
	}
	if content := readTestFile(t, root, "file.txt"); content != before {
		t.Fatalf("content = %q, want unchanged %q", content, before)
	}
}

func TestMekugi2NewFileInitializerIsImmediate(t *testing.T) {
	tests := []struct{ name, script, wantMessage string }{
		{"existing", "in existing.txt\ntype \"new\"", "bare type VALUE only initializes the immediately preceding new; editing an existing file requires a line, range, or text target"},
		{"intervening", "new new.txt\nin existing.txt\nin new.txt\ntype \"new\"", ""},
		{"second", "new new.txt\ntype \"one\"\ntype \"two\"", ""},
		{"target new", "new new.txt\ntype \"one\\n\"\ntype " + row(1, "one") + ` "ONE"`, ""},
		{"append new", "new new.txt\nadd EOF \"one\\n\"", ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, root, "existing.txt", "old\n", 0o644)
			result, err := applyForHostAtTest(t, root, test.script, "")
			if err == nil || !strings.Contains(result.Diagnostic, "initialization") {
				t.Fatalf("ApplyForHost() error = %v, diagnostic %q", err, result.Diagnostic)
			}
			if test.wantMessage != "" && !strings.Contains(result.Diagnostic, test.wantMessage) {
				t.Fatalf("diagnostic = %q, want message %q", result.Diagnostic, test.wantMessage)
			}
			if got := readTree(t, root); !reflect.DeepEqual(got, map[string]string{"existing.txt": "old\n"}) {
				t.Fatalf("rejection changed tree: %#v", got)
			}
		})
	}
}

func TestMekugi2RejectsInvalidMutationForms(t *testing.T) {
	for _, command := range []string{
		"add " + row(1, "x") + ".." + row(1, "x") + ` "value"`,
		`type EOF "value"`,
		"type <<BODY\nx\nWRONG",
	} {
		t.Run(strings.Fields(command)[0], func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, root, "file.txt", "x\n", 0o644)
			result, err := applyForHostAtTest(t, root, "in file.txt\n"+command, "")
			if err == nil || !strings.Contains(result.Diagnostic, "reason script-syntax") {
				t.Fatalf("ApplyForHost() error = %v, diagnostic %q", err, result.Diagnostic)
			}
		})
	}
}

func TestMekugi2FixedHeredocAndInlineInsertion(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "file.txt", "target\n", 0o644)
	script := "in file.txt\n" +
		"add " + row(1, "target") + ` "// comment\n"` + "\n" +
		"add EOF <<PATCH\nmultiline\nvalue\nPATCH\n"
	result, err := applyForHostAtTest(t, root, script, "")
	if err != nil {
		t.Fatalf("ApplyForHost() error = %v, diagnostic %q", err, result.Diagnostic)
	}
	want := "// comment\ntarget\nmultiline\nvalue\n"
	if got := readTestFile(t, root, "file.txt"); got != want {
		t.Fatalf("file = %q, want %q", got, want)
	}
}

func TestMekugi2HeredocValueSupportsTextTarget(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "file.txt", "prefix needle suffix\n", 0o644)
	script := "in file.txt\ntype " + row(1, "prefix needle suffix") + " \"needle\" <<PATCH\n" +
		"multiline\nvalue\nPATCH\n"
	result, err := applyForHostAtTest(t, root, script, "")
	if err != nil {
		t.Fatalf("ApplyForHost() error = %v, diagnostic %q", err, result.Diagnostic)
	}
	want := "prefix multiline\nvalue\n suffix\n"
	if got := readTestFile(t, root, "file.txt"); got != want {
		t.Fatalf("file = %q, want %q", got, want)
	}
}

func TestMekugi2UnanchoredLiteralTargetsUseImmutableBaseline(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "file.txt", "alpha x\nbeta x\n", 0o644)
	script := strings.Join([]string{
		"in file.txt",
		`type "alpha" "ALPHA"`,
		`add "x" 2 "!"`,
	}, "\n")
	result, err := applyForHostAtTest(t, root, script, "")
	if err != nil {
		t.Fatalf("ApplyForHost() error = %v, diagnostic %q", err, result.Diagnostic)
	}
	if got, want := readTestFile(t, root, "file.txt"), "ALPHA !x\nbeta !x\n"; got != want {
		t.Fatalf("file = %q, want %q", got, want)
	}
}

func TestMekugi2UnanchoredLiteralHeredocAndMissingOccurrence(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "file.txt", "before needle after\n", 0o644)
	script := "in file.txt\ntype \"needle\" <<PATCH\nmultiline\nvalue\nPATCH\n"
	result, err := applyForHostAtTest(t, root, script, "")
	if err != nil {
		t.Fatalf("ApplyForHost() error = %v, diagnostic %q", err, result.Diagnostic)
	}
	if got, want := readTestFile(t, root, "file.txt"), "before multiline\nvalue\n after\n"; got != want {
		t.Fatalf("file = %q, want %q", got, want)
	}

	writeTestFile(t, root, "missing.txt", "one x\n", 0o644)
	before := readTestFile(t, root, "missing.txt")
	result, err = applyForHostAtTest(t, root, "in missing.txt\ntype \"x\" 2 \"y\"", "")
	if err == nil || !strings.Contains(result.Diagnostic, "occurrence-missing") {
		t.Fatalf("ApplyForHost() error = %v, diagnostic %q", err, result.Diagnostic)
	}
	if got := readTestFile(t, root, "missing.txt"); got != before {
		t.Fatalf("rejection changed file to %q", got)
	}
}

func TestMekugi2QuotedDoubleLessRemainsInlineText(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		initial string
		script  func(string) string
		want    string
	}{
		{
			name: "initializer",
			path: "file.txt",
			script: func(string) string {
				return `new file.txt` + "\n" + `type "a << b"`
			},
			want: "a << b",
		},
		{
			name: "initializer after escaped quote",
			path: "file.txt",
			script: func(string) string {
				return `new file.txt` + "\n" + `type "a \"<< b"`
			},
			want: `a "<< b`,
		},
		{
			name:    "replacement value",
			path:    "file.txt",
			initial: "old\n",
			script: func(string) string {
				return "in file.txt\ntype " + row(1, "old") + ` "a << b"`
			},
			want: "a << b\n",
		},
		{
			name:    "target literal",
			path:    "file.txt",
			initial: "a << b\n",
			script: func(string) string {
				return "in file.txt\ntype " + row(1, "a << b") + ` "a << b" "changed"`
			},
			want: "changed\n",
		},
		{
			name:    "path",
			path:    "a<<b.txt",
			initial: "old\n",
			script: func(string) string {
				return "in a<<b.txt\ntype " + row(1, "old") + ` "changed"`
			},
			want: "changed\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if test.initial != "" {
				writeTestFile(t, root, test.path, test.initial, 0o644)
			}
			result, err := applyForHostAtTest(t, root, test.script(test.path), "")
			if err != nil {
				t.Fatalf("ApplyForHost() error = %v, diagnostic %q", err, result.Diagnostic)
			}
			if got := readTestFile(t, root, test.path); got != test.want {
				t.Fatalf("file = %q, want %q", got, test.want)
			}
		})
	}
}

func TestMekugi2InvalidHeredocIsOneHeaderOwnedFailure(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "file.txt", "unchanged\n", 0o644)
	script := "in file.txt\ntype " + row(1, "unchanged") + " <<BODY\n" +
		"rm\nWRONG\n"
	result, err := applyForHostAtTest(t, root, script, "")
	if err == nil {
		t.Fatalf("ApplyForHost() error = %v, diagnostic %q", err, result.Diagnostic)
	}
	if strings.Count(result.Diagnostic, ": command") != 1 ||
		!strings.Contains(result.Diagnostic, "command 2") ||
		!strings.Contains(result.Diagnostic, "unterminated heredoc") {
		t.Fatalf("diagnostic = %q", result.Diagnostic)
	}
	if got := readTestFile(t, root, "file.txt"); got != "unchanged\n" {
		t.Fatalf("rejection changed file to %q", got)
	}
}

func TestMekugi2TargetLiteralRejectsC0ControlsExceptTab(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "file.txt", "a\tb\n", 0o644)
	valid := "in file.txt\ntype " + row(1, "a\tb") + ` "a\tb" "ok"`
	result, err := applyForHostAtTest(t, root, valid, "")
	if err != nil {
		t.Fatalf("tab target error = %v, diagnostic %q", err, result.Diagnostic)
	}

	writeTestFile(t, root, "file.txt", "a\x01b\n", 0o644)
	invalid := "in file.txt\ntype " + row(1, "a\x01b") + ` "a\u0001b" "bad"`
	result, err = applyForHostAtTest(t, root, invalid, "")
	if err == nil || !strings.Contains(result.Diagnostic, "forbidden control character") {
		t.Fatalf("control target error = %v, diagnostic %q", err, result.Diagnostic)
	}
}

func TestParseTargetIdentityUsesRootTargetSemantics(t *testing.T) {
	parseExact := func(target string) TargetIdentity {
		t.Helper()
		identity, trailing, err := ParseTargetIdentity(target, false)
		if err != nil || trailing != "" {
			t.Fatalf("ParseTargetIdentity(%q) = %+v, %q, %v", target, identity, trailing, err)
		}
		return identity
	}

	for _, equivalent := range [][2]string{
		{`1:aaaa`, `1:aaaa..1:aaaa`},
		{`"old\u000Atext" 1`, `"old\ntext"`},
		{`1:aaaa "old\u000Atext" 1`, `1:aaaa "old\ntext"`},
	} {
		if first, second := parseExact(equivalent[0]), parseExact(equivalent[1]); first != second {
			t.Errorf("target identities differ for %q and %q", equivalent[0], equivalent[1])
		}
	}

	for _, target := range []string{`"line\ntext"`, `"line\u000Atext"`, `1:aaaa "line\ntext"`, `"tab\ttext"`} {
		parseExact(target)
	}
	for _, target := range []string{`""`, "\"raw\nnewline\"", `"return\r"`, `"return\u000D"`, `"control\u0001"`, `EOF`} {
		if _, _, err := ParseTargetIdentity(target, false); err == nil {
			t.Errorf("ParseTargetIdentity(%q) unexpectedly succeeded", target)
		}
	}

	identity, trailing, err := ParseTargetIdentity(`1:aaaa "value"`, true)
	if err != nil || identity != parseExact(`1:aaaa`) || trailing != `"value"` {
		t.Fatalf("mutation target = %+v, %q, %v", identity, trailing, err)
	}
}

func TestMekugi2MultilineLiteralTargets(t *testing.T) {
	tests := []struct {
		name    string
		content string
		script  string
		want    string
	}{
		{
			name:    "unanchored replacement",
			content: "before\nfirst\nsecond\nafter\n",
			script:  `in file.txt` + "\n" + `type "first\nsecond" "replacement"`,
			want:    "before\nreplacement\nafter\n",
		},
		{
			name:    "row anchored replacement",
			content: "skip first\nfirst\nsecond\nafter\n",
			script:  "in file.txt\ntype " + row(2, "first") + ` "first\nsecond" "replacement"`,
			want:    "skip first\nreplacement\nafter\n",
		},
		{
			name:    "unicode escaped line feed",
			content: "first\nsecond\n",
			script:  `in file.txt` + "\n" + `type "first\u000Asecond" "replacement"`,
			want:    "replacement\n",
		},
		{
			name:    "occurrence count",
			content: "first\nsecond\nfirst\nsecond\n",
			script:  `in file.txt` + "\n" + `type "first\nsecond" 2 "replacement"`,
			want:    "replacement\nreplacement\n",
		},
		{
			name:    "target includes trailing LF",
			content: "first\nsecond\n",
			script:  `in file.txt` + "\n" + `type "first\n" "FIRST\n"`,
			want:    "FIRST\nsecond\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, root, "file.txt", test.content, 0o644)
			result, err := applyForHostAtTest(t, root, test.script, "")
			if err != nil {
				t.Fatalf("ApplyForHost() error = %v, diagnostic %q", err, result.Diagnostic)
			}
			if got := readTestFile(t, root, "file.txt"); got != test.want {
				t.Fatalf("file = %q, want %q", got, test.want)
			}
		})
	}
}

func TestMekugi2MultilineLiteralTargetRejectionsAreAtomic(t *testing.T) {
	tests := []struct {
		name, content, script, diagnostic string
	}{
		{
			name:       "missing exact multiline text",
			content:    "first\nchanged\n",
			script:     `in file.txt` + "\n" + `type "first\nsecond" "replacement"`,
			diagnostic: "occurrence-missing",
		},
		{
			name:       "missing trailing LF",
			content:    "first",
			script:     `in file.txt` + "\n" + `type "first\n" "replacement"`,
			diagnostic: "occurrence-missing",
		},
		{
			name:       "ambiguous stale anchor",
			content:    "first\nsecond\nfirst\nsecond\n",
			script:     `in file.txt` + "\n" + `type 1:ffff "first\nsecond" "replacement"`,
			diagnostic: "row-stale",
		},
		{
			name:       "raw physical newline",
			content:    "first\nsecond\n",
			script:     "in file.txt\ntype \"first\nsecond\" \"replacement\"",
			diagnostic: "script-syntax",
		},
		{
			name:       "escaped carriage return",
			content:    "first\rsecond\n",
			script:     `in file.txt` + "\n" + `type "first\rsecond" "replacement"`,
			diagnostic: "forbidden carriage return",
		},
		{
			name:       "unicode carriage return",
			content:    "first\rsecond\n",
			script:     `in file.txt` + "\n" + `type "first\u000Dsecond" "replacement"`,
			diagnostic: "forbidden carriage return",
		},
		{
			name:       "forbidden control",
			content:    "first\x01second\n",
			script:     `in file.txt` + "\n" + `type "first\u0001second" "replacement"`,
			diagnostic: "forbidden control character",
		},
		{
			name:       "empty target",
			content:    "first\n",
			script:     `in file.txt` + "\n" + `type "" "replacement"`,
			diagnostic: "target literal must not be empty",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, root, "file.txt", test.content, 0o644)
			result, err := applyForHostAtTest(t, root, test.script, "")
			if err == nil || !strings.Contains(result.Diagnostic, test.diagnostic) {
				t.Fatalf("ApplyForHost() error = %v, diagnostic %q; want %q", err, result.Diagnostic, test.diagnostic)
			}
			if got := readTestFile(t, root, "file.txt"); got != test.content {
				t.Fatalf("rejection changed file to %q", got)
			}
		})
	}
}

func row(line int, content string) string {
	return fmt.Sprintf("%d:%s", line, hashLine(content))
}

func applyForHostAtTest(t *testing.T, rootPath, script, dataDirectory string) (HostTranslation, error) {
	t.Helper()
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	return ApplyForHost(t.Context(), Workspace{Root: root}, script, dataDirectory)
}

func translateForHostAtTest(t *testing.T, rootPath, script, dataDirectory string) (HostTranslation, error) {
	t.Helper()
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	return translateForHostForTest(t.Context(), Workspace{Root: root}, script, dataDirectory)
}

func writeTestFile(t *testing.T, root, path, content string, mode fs.FileMode) {
	t.Helper()
	fullPath := filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fullPath, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func readTestFile(t *testing.T, root, path string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(root, path))
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func readTree(t *testing.T, root string) map[string]string {
	t.Helper()
	files := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[relative] = string(content)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func testFileMode(t *testing.T, path string) fs.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

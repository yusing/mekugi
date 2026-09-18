package mekugi

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func fileEditRow(line int, content string) string {
	return strconv.Itoa(line) + ":" + hashLine(content)
}

func TestFileEditRejectsEmptyOuterPath(t *testing.T) {
	directory := t.TempDir()
	_, err := TranslateForHostAt(t.Context(), directory, []FileEdit{{
		Script: `append "value"`,
	}}, "")
	if err == nil || !strings.Contains(err.Error(), "file edit path must not be empty") {
		t.Fatalf("empty outer path error = %v", err)
	}
}

func TestFileEditRejectsMissingPath(t *testing.T) {
	directory := t.TempDir()
	_, err := TranslateForHostAt(t.Context(), directory, []FileEdit{{
		Path:   "missing.txt",
		Script: `append "value"`,
	}}, "")
	if err == nil || !strings.Contains(err.Error(), "missing.txt") {
		t.Fatalf("missing path error = %v", err)
	}
}

func TestFileEditsInterleaveRepeatedPathsAgainstImmutableBaselines(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "a.txt"), []byte("a1\na2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "b.txt"), []byte("b\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := ApplyForHostAt(t.Context(), directory, []FileEdit{
		{Path: "a.txt", Script: `type ` + fileEditRow(1, "a1") + ` "A1"`},
		{Path: "b.txt", Script: `type ` + fileEditRow(1, "b") + ` "B"`},
		{Path: "a.txt", Script: `type ` + fileEditRow(2, "a2") + ` "A2"`},
	}, "")
	if err != nil || !result.Change.Applied {
		t.Fatalf("interleaved edit result = %+v, error = %v", result, err)
	}
	if got, err := os.ReadFile(filepath.Join(directory, "a.txt")); err != nil || string(got) != "A1\nA2\n" {
		t.Fatalf("a.txt = %q, error = %v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(directory, "b.txt")); err != nil || string(got) != "B\n" {
		t.Fatalf("b.txt = %q, error = %v", got, err)
	}
}

func TestFileEditsAppendInCommandOrder(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "append.txt")
	if err := os.WriteFile(path, []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := ApplyForHostAt(t.Context(), directory, []FileEdit{
		{Path: "append.txt", Script: `append "one\n"`},
		{Path: "append.txt", Script: `append "two\n"`},
		{Path: "append.txt", Script: `append "three\n"`},
	}, "")
	if err != nil || !result.Change.Applied {
		t.Fatalf("append result = %+v, error = %v", result, err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "base\none\ntwo\nthree\n" {
		t.Fatalf("append.txt = %q, error = %v", got, err)
	}
}

func TestFileEditsRejectSyntaxAtomicallyAcrossPaths(t *testing.T) {
	directory := t.TempDir()
	for name, content := range map[string]string{"a.txt": "a\n", "b.txt": "b\n"} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	result, err := ApplyForHostAt(t.Context(), directory, []FileEdit{
		{Path: "a.txt", Script: `type ` + fileEditRow(1, "a") + ` "A"`},
		{Path: "b.txt", Script: "removed-command\n"},
	}, "")
	if err == nil || result.Change.Applied || !strings.Contains(err.Error(), `path "b.txt"`) {
		t.Fatalf("syntax rejection result = %+v, error = %v", result, err)
	}
	for name, want := range map[string]string{"a.txt": "a\n", "b.txt": "b\n"} {
		if got, readErr := os.ReadFile(filepath.Join(directory, name)); readErr != nil || string(got) != want {
			t.Fatalf("%s = %q, error = %v", name, got, readErr)
		}
	}
}

func TestFileEditsRejectRemovedLifecycleCommandsAndEOF(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "file.txt")
	if err := os.WriteFile(path, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, script := range []string{
		"in file.txt\n",
		"new file.txt\n",
		"mv other.txt\n",
		"rm\n",
		`add EOF "value"`,
	} {
		t.Run(strings.Fields(script)[0], func(t *testing.T) {
			_, err := TranslateForHostAt(t.Context(), directory, []FileEdit{{
				Path:   "file.txt",
				Script: script,
			}}, "")
			if err == nil {
				t.Fatalf("removed command %q was accepted", script)
			}
		})
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "old\n" {
		t.Fatalf("file.txt = %q, error = %v", got, err)
	}
}

func TestFileEditsIsolateHeredocFramingAcrossPaths(t *testing.T) {
	directory := t.TempDir()
	firstPath := filepath.Join(directory, "quoted name.txt")
	secondPath := filepath.Join(directory, "second.txt")
	if err := os.WriteFile(firstPath, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secondPath, []byte("old2\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := ApplyForHostAt(t.Context(), directory, []FileEdit{
		{
			Path: "quoted name.txt",
			Script: "type " + fileEditRow(1, "old") + " <<END\n" +
				"quoted \"value\"\n" +
				"END",
		},
		{
			Path: "second.txt",
			Script: "type " + fileEditRow(1, "old2") + " <<END\n" +
				"second value\n" +
				"END\n",
		},
	}, "")
	if err != nil || !result.Change.Applied {
		t.Fatalf("heredoc result = %+v, error = %v", result, err)
	}
	if got, err := os.ReadFile(firstPath); err != nil || string(got) != "quoted \"value\"\n" {
		t.Fatalf("quoted name.txt = %q, error = %v", got, err)
	}
	if got, err := os.ReadFile(secondPath); err != nil || string(got) != "second value\n" {
		t.Fatalf("second.txt = %q, error = %v", got, err)
	}
}

func TestPreviewForHostAtUsesFileEditPathsWithoutApplying(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "preview.txt")
	if err := os.WriteFile(path, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	files, err := PreviewForHostAt(t.Context(), directory, []FileEdit{{
		Path:   "preview.txt",
		Script: `type ` + fileEditRow(1, "old") + ` "new"`,
	}})
	if err != nil || len(files) != 1 {
		t.Fatalf("preview files = %+v, error = %v", files, err)
	}
	if files[0].BeforePath != path || files[0].AfterPath != path || !strings.Contains(files[0].Diff, "+new") {
		t.Fatalf("preview file = %+v", files[0])
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "old\n" {
		t.Fatalf("preview changed source to %q, error = %v", got, err)
	}
}

func TestEditTextAcceptsOnlyPathlessTargetMutations(t *testing.T) {
	edited, err := EditText(t.Context(), "old\n", `type `+fileEditRow(1, "old")+` "new"`)
	if err != nil || edited != "new\n" {
		t.Fatalf("EditText = %q, error = %v", edited, err)
	}
	if _, err := EditText(t.Context(), "old\n", `append "new"`); err == nil {
		t.Fatal("EditText accepted append")
	}
	if _, err := EditText(t.Context(), "old\n", "in file.txt\n"); err == nil {
		t.Fatal("EditText accepted a lifecycle command")
	}
}

func TestFileEditsValidateBlankEntriesBeforeMutation(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "a.txt")
	if err := os.WriteFile(path, []byte("a\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := ApplyForHostAt(t.Context(), directory, []FileEdit{
		{Path: "a.txt", Script: `type ` + fileEditRow(1, "a") + ` "A"`},
		{Path: "missing.txt"},
	}, "")
	if err == nil || result.Change.Applied || !strings.Contains(err.Error(), "missing.txt") {
		t.Fatalf("blank missing entry result = %+v, error = %v", result, err)
	}
	if got, readErr := os.ReadFile(path); readErr != nil || string(got) != "a\n" {
		t.Fatalf("blank missing entry changed a.txt to %q, error = %v", got, readErr)
	}

	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	result, err = ApplyForHost(t.Context(), Workspace{Root: root}, []FileEdit{
		{Path: "a.txt", Script: `type ` + fileEditRow(1, "a") + ` "A"`},
		{Path: "../outside.txt"},
	}, "")
	if err == nil || result.Change.Applied || !strings.Contains(err.Error(), "outside workspace root") {
		t.Fatalf("blank confined entry result = %+v, error = %v", result, err)
	}
	if got, readErr := os.ReadFile(path); readErr != nil || string(got) != "a\n" {
		t.Fatalf("blank confined entry changed a.txt to %q, error = %v", got, readErr)
	}
}

func TestHostFileEditsNormalizeRelativeAndAbsoluteIdentity(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "a.txt")
	if err := os.WriteFile(path, []byte("a\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := ApplyForHostAt(t.Context(), directory, []FileEdit{
		{Path: "a.txt", Script: `type ` + fileEditRow(1, "a") + ` "A"`},
		{Path: path, Script: `append "one\n"`},
		{Path: "a.txt", Script: `append "two\n"`},
	}, "")
	if err != nil || !result.Change.Applied || result.Change.Files != 1 {
		t.Fatalf("relative/absolute identity result = %+v, error = %v", result, err)
	}
	if got, readErr := os.ReadFile(path); readErr != nil || string(got) != "A\none\ntwo\n" {
		t.Fatalf("normalized identity content = %q, error = %v", got, readErr)
	}
}

func TestPreviewForHostAtBoundsAppendMutations(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "append.txt")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var script strings.Builder
	for i := 0; i < 1025; i++ {
		script.WriteString(`append "x"`)
		script.WriteByte('\n')
	}
	_, err := PreviewForHostAt(t.Context(), directory, []FileEdit{{
		Path:   "append.txt",
		Script: script.String(),
	}})
	if err == nil || !strings.Contains(err.Error(), "target mutations") {
		t.Fatalf("append mutation budget error = %v", err)
	}
}

func TestAppendFailureRetainsOperationDiagnostic(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "append.txt")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := ApplyForHostAt(t.Context(), directory, []FileEdit{{
		Path:   "append.txt",
		Script: "append \n",
	}}, "")
	if err == nil || len(result.Rejections) != 1 || result.Rejections[0].Operation != "append" {
		t.Fatalf("append failure result = %+v, error = %v", result, err)
	}
}

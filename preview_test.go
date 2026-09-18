package mekugi

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStreamingPreviewPartialValues(t *testing.T) {
	for _, tc := range []struct{ name, input, want string }{
		{"quote", "in file.txt\ntype \"old\" \"hel", "+hel"},
		{"escape", "in file.txt\ntype \"old\" \"hello\\", "+hello"},
		{"heredoc", "in file.txt\ntype \"old\" <<PATCH\nhello\nwor", "+wor"},
		{"quoted heredoc", "in file.txt\ntype \"old\" <<'END'\nhello\nwor", "+wor"},
		{"tab stripped heredoc", "in file.txt\ntype \"old\" <<-END\n\thello\n\twor", "+wor"},
		{"new", "new added.go\ntype <<PATCH\npackage main\nfunc incom", "+func incom"},
		{"shell", "in file.txt\ntype \"old\" \"hello\"\nshell touch SHOULD_NOT_EXIST\nnew unseen\ntype \"x\"", "+hello"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			directory := t.TempDir()
			path := filepath.Join(directory, "file.txt")
			if err := os.WriteFile(path, []byte("old\n"), 0600); err != nil {
				t.Fatal(err)
			}
			files, err := PreviewForHostAt(t.Context(), directory, tc.input)
			if err != nil || len(files) != 1 || !strings.Contains(files[0].Diff, tc.want) {
				t.Fatalf("files=%+v err=%v, want %s", files, err, tc.want)
			}
			content, _ := os.ReadFile(path)
			if string(content) != "old\n" {
				t.Fatal("preview mutated the source")
			}
			entries, _ := os.ReadDir(directory)
			if len(entries) != 1 {
				t.Fatal("preview created files")
			}
		})
	}
}

func TestStreamingPreviewEveryPrefix(t *testing.T) {
	directory := t.TempDir()
	script := "new file.txt\ntype <<-'END'\n\thello\n\tworld\n\tEND\n"
	for end := 0; end <= len(script); end++ {
		_, err := PreviewForHostAt(t.Context(), directory, script[:end])
		if err != nil {
			t.Fatalf("prefix %q: %v", script[:end], err)
		}
	}
}

func TestStreamingPreviewDoesNotInterpretPayloadAsCommands(t *testing.T) {
	directory := t.TempDir()
	for _, script := range []string{
		"new file.txt\ntype <<TEXT\n|shell touch forbidden\n|new other\n|type \"x\"\n",
		"new file.txt\ntype <<PATCH\nin other\nrm\n",
		"new file.txt\ntype \"line\\nnew other\\nrm",
	} {
		files, err := PreviewForHostAt(t.Context(), directory, script)
		if err != nil || len(files) != 1 || files[0].AfterPath != filepath.Join(directory, "file.txt") {
			t.Fatalf("payload was interpreted: %+v, %v", files, err)
		}
	}
}

func TestStreamingPreviewBoundsAndDirectory(t *testing.T) {
	directory := t.TempDir()
	if _, err := PreviewForHostAt(t.Context(), "", "in file.txt\n"); err == nil {
		t.Fatal("relative preview borrowed process cwd")
	}
	if _, err := PreviewForHostAt(t.Context(), directory, strings.Repeat(" ", 256<<10+1)); err == nil {
		t.Fatal("oversized input accepted")
	}
	if err := os.WriteFile(filepath.Join(directory, "large.txt"), []byte(strings.Repeat("x", 256<<10+1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := PreviewForHostAt(t.Context(), directory, "in large.txt\n"); err == nil {
		t.Fatal("oversized source accepted")
	}
}

func TestStreamingPreviewBoundsExpandedTargetWork(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "file.txt")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 128<<10)), 0600); err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{
		"in file.txt\ntype \"x\" 131072 \"y",
		"in file.txt\ntype \"x\" 1024 \"y\"\nadd EOF \"z",
	} {
		if _, err := PreviewForHostAt(t.Context(), directory, input); err == nil || !strings.Contains(err.Error(), "target mutations") {
			t.Fatalf("unbounded speculative target work: %v", err)
		}
	}
}

func TestStreamingPreviewMixedInput(t *testing.T) {
	directory := t.TempDir()
	prefix := "new before.txt\ntype \"before\\n\"\n"
	for _, tail := range []string{
		"shell",
		"shell printf live",
		"shell <<SHELL\nprintf 'streaming",
		"shell <<SHELL\nprintf live\nSHELL\nnew after.txt\ntype \"after",
		"resume M0123456789abcdef0123456789abcdef\n",
	} {
		preview, err := PreviewScriptForHostAt(t.Context(), directory, prefix+tail)
		if err != nil || preview.PendingInput != tail || len(preview.Files) != 1 ||
			preview.Files[0].AfterPath != filepath.Join(directory, "before.txt") {
			t.Fatalf("mixed preview: %+v, %v", preview, err)
		}
	}
	// Apparent shell commands inside edit payloads remain file content.
	preview, err := PreviewScriptForHostAt(t.Context(), directory, "new body.txt\ntype <<PATCH\nshell touch forbidden\n")
	if err != nil || preview.PendingInput != "" || len(preview.Files) != 1 {
		t.Fatalf("payload became script: %+v, %v", preview, err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 0 {
		t.Fatal("mixed preview executed effects")
	}
}

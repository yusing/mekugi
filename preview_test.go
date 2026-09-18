package mekugi

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStreamingPreviewPartialValues(t *testing.T) {
	for _, tc := range []struct{ name, input, want string }{
		{"quote", "type \"old\" \"hel", "+hel"},
		{"escape", "type \"old\" \"hello\\", "+hello"},
		{"heredoc", "type \"old\" <<PATCH\nhello\nwor", "+wor"},
		{"quoted heredoc", "type \"old\" <<'END'\nhello\nwor", "+wor"},
		{"tab stripped heredoc", "type \"old\" <<-END\n\thello\n\twor", "+wor"},
		{"append", "append <<PATCH\npackage main\nfunc incom", "+func incom"},
		{"payload", "type \"old\" \"hello\"", "+hello"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			directory := t.TempDir()
			path := filepath.Join(directory, "file.txt")
			if err := os.WriteFile(path, []byte("old\n"), 0600); err != nil {
				t.Fatal(err)
			}
			files, err := PreviewForHostAt(t.Context(), directory, []FileEdit{{Path: "file.txt", Script: tc.input}})
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
	if err := os.WriteFile(filepath.Join(directory, "file.txt"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	script := "append <<-'END'\n\thello\n\tworld\n\tEND\n"
	for end := 0; end <= len(script); end++ {
		_, err := PreviewForHostAt(t.Context(), directory, []FileEdit{{Path: "file.txt", Script: script[:end]}})
		if err != nil {
			t.Fatalf("prefix %q: %v", script[:end], err)
		}
	}
}

func TestStreamingPreviewDoesNotInterpretPayloadAsCommands(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "file.txt"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, script := range []string{
		"append <<TEXT\n|shell touch forbidden\n|new other\n|type \"x\"\n",
		"append <<PATCH\nin other\nrm\n",
		"append \"line\\nnew other\\nrm\"",
	} {
		files, err := PreviewForHostAt(t.Context(), directory, []FileEdit{{Path: "file.txt", Script: script}})
		if err != nil || len(files) != 1 || files[0].AfterPath != filepath.Join(directory, "file.txt") {
			t.Fatalf("payload was interpreted: %+v, %v", files, err)
		}
	}
}

func TestStreamingPreviewBoundsAndDirectory(t *testing.T) {
	directory := t.TempDir()
	if _, err := PreviewForHostAt(t.Context(), "", []FileEdit{{Path: "file.txt", Script: ""}}); err == nil {
		t.Fatal("relative preview borrowed process cwd")
	}
	if _, err := PreviewForHostAt(t.Context(), directory, []FileEdit{{Path: "file.txt", Script: strings.Repeat(" ", 256<<10+1)}}); err == nil {
		t.Fatal("oversized input accepted")
	}
	if err := os.WriteFile(filepath.Join(directory, "large.txt"), []byte(strings.Repeat("x", 256<<10+1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := PreviewForHostAt(t.Context(), directory, []FileEdit{{Path: "large.txt", Script: ""}}); err == nil {
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
		"type \"x\" 131072 \"y\"",
		"type \"x\" 1024 \"y\"\nappend \"z\"",
	} {
		if _, err := PreviewForHostAt(t.Context(), directory, []FileEdit{{Path: "file.txt", Script: input}}); err == nil || !strings.Contains(err.Error(), "target mutations") {
			t.Fatalf("unbounded speculative target work: %v", err)
		}
	}
}

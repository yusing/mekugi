package router

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi"
)

func TestLiveDiffShellEditLiteralInputs(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"\n# edit\nhpatch<<'EDIT' # literal input\nnew file.txt\ntype \"hel", "new file.txt\ntype \"hel"},
		{"hpatch <<'EDIT'\nnew file.txt\ntype \"hel", "new file.txt\ntype \"hel"},
		{"hpatch <<'EDIT'\nnew file.txt\ntype \"hello\"\nED", "new file.txt\ntype \"hello\"\n"},
		{"hpatch <<'EDIT'\nnew file.txt\ntype \"hello\"\nEDIT\n", "new file.txt\ntype \"hello\"\n"},
		{"hpatch <<-'EDIT'\n\tnew file.txt\n\ttype \"hel", "new file.txt\ntype \"hel"},
		{"hpatch 'new file.txt\ntype \"hel", "new file.txt\ntype \"hel"},
		{`hpatch $'new file.txt\ntype "hello"'`, "new file.txt\ntype \"hello\""},
		{"hpatch \"new file.txt\ntype \\\"hel", "new file.txt\ntype \"hel"},
		{"#!sh\nhpatch <<'EDIT'\nnew file.txt\ntype \"$literal", "new file.txt\ntype \"$literal"},
		{"hpatch <<EDIT\nnew file.txt\ntype \"hello\"\n", "new file.txt\ntype \"hello\"\n"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			directory := t.TempDir()
			got, base, ok := liveDiffShellEdit(tc.input, directory)
			if !ok || got != tc.want || base != directory {
				t.Fatalf("decode = %q, %q, %t; want %q", got, base, ok, tc.want)
			}
			files, err := mekugi.PreviewForHostAt(t.Context(), base, got)
			if err != nil || len(files) != 1 || !strings.Contains(files[0].Diff, "+") {
				t.Fatalf("preview = %+v, %v", files, err)
			}
			entries, err := os.ReadDir(directory)
			if err != nil || len(entries) != 0 {
				t.Fatalf("preview modified workspace: %v, %v", entries, err)
			}
		})
	}
}

func TestLiveDiffShellEditNoDynamicExecution(t *testing.T) {
	for _, input := range []string{
		"hpatch --recover amber 'maple target \"new\"'",
		"hpatch \"$(touch forbidden)\"",
		"hpatch <<EDIT\n$(touch forbidden)\n",
		"hpatch <<EDIT\n$PAYLOAD\n",
		"hpatch < source.patch",
		"hpatch 'new file' > output",
		"echo prefix; hpatch 'new file'",
		"hpatch 'new file'; echo suffix",
		"hpatch 'new file' &",
		"! hpatch 'new file'",
		"PAYLOAD=x hpatch 'new file'",
		"#!python3\nhpatch 'new file'",
		"#!cmd=cat input | {.}\nhpatch 'new file'",
		"hpatch 'new file'\n#!python3\nprint('suffix')",
	} {
		if source, _, ok := liveDiffShellEdit(input, t.TempDir()); ok {
			t.Errorf("projected execution-dependent input %q as %q", input, source)
		}
	}
}

func TestLiveDiffShellEditWorkdir(t *testing.T) {
	directory := t.TempDir()
	source, base, ok := liveDiffShellEdit("#!params="+string(mustMarshalJSON(map[string]any{"workdir": directory}))+"\nhpatch 'new file.txt\ntype \"hello\"'", "")
	if !ok || base != directory {
		t.Fatalf("workdir = %q, %t", base, ok)
	}
	files, err := mekugi.PreviewForHostAt(t.Context(), base, source)
	if err != nil || len(files) != 1 || files[0].AfterPath != filepath.Join(directory, "file.txt") {
		t.Fatalf("files = %+v, %v", files, err)
	}
}

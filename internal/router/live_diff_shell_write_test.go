package router

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi"
)

func testLiveDiffShellWrite(ctx context.Context, input, directory string) ([]mekugi.ReviewFile, bool, error) {
	statements, directory, partial, ok := liveDiffShellStatements(input, directory)
	if !ok || len(statements) != 1 {
		return nil, false, nil
	}
	return liveDiffShellWriteStatement(ctx, statements[0], directory, partial, true)
}

func TestLiveDiffShellWriteLiteralInputs(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, input, body string
		appendMode        bool
	}{
		{"unfinished", "cat >'file name.txt' <<'END'\nhello", "hello", false},
		{"closed", "cat <<'END' >'file name.txt'\nhello\nEND\n", "hello\n", false},
		{"clobber", "cat >|'file name.txt' <<'END'\nhello\nEND\n", "hello\n", false},
		{"append", "cat >>'file name.txt' <<'END'\nhello\nEND\n", "hello\n", true},
		{"quoted expansion", "cat >'file name.txt' <<'END'\n$HOME $(touch marker) `id`\nEND\n", "$HOME $(touch marker) `id`\n", false},
		{"safe unquoted escapes", "cat >'file name.txt' <<END\n\\$HOME \\`id\\` \\\\hello\nEND\n", "$HOME `id` \\hello\n", false},
		{"tab stripping", "cat >'file name.txt' <<-'END'\n\thello\n\tEND\n", "hello\n", false},
		{"double quoted delimiter", "cat >\"file name.txt\" <<\"END\"\n$HOME\nEND\n", "$HOME\n", false},
	} {
		for _, exists := range []bool{false, true} {
			name := tc.name + "/new"
			if exists {
				name = tc.name + "/existing"
			}
			t.Run(name, func(t *testing.T) {
				directory := t.TempDir()
				path := filepath.Join(directory, "file name.txt")
				if exists {
					if err := os.WriteFile(path, []byte("old\n"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				files, decoded, err := testLiveDiffShellWrite(t.Context(), tc.input, directory)
				if err != nil || !decoded || len(files) != 1 {
					t.Fatalf("projection = %+v, %t, %v", files, decoded, err)
				}
				file := files[0]
				if file.AfterPath != path && file.AfterPath != "file name.txt" {
					t.Fatalf("unexpected after path %q", file.AfterPath)
				}
				for line := range strings.SplitSeq(strings.TrimSuffix(tc.body, "\n"), "\n") {
					if !strings.Contains(file.Diff, "+"+line) {
						t.Fatalf("missing added line %q in %q", line, file.Diff)
					}
				}
				if exists && !tc.appendMode && !strings.Contains(file.Diff, "-old") {
					t.Fatalf("truncate did not remove old content: %q", file.Diff)
				}
				if exists && tc.appendMode && strings.Contains(file.Diff, "-old") {
					t.Fatalf("append removed old content: %q", file.Diff)
				}
				if !exists && file.BeforePath != "" {
					t.Fatalf("new file has before path %q", file.BeforePath)
				}
				data, readErr := os.ReadFile(path)
				if exists && (readErr != nil || string(data) != "old\n") {
					t.Fatalf("preview changed file: %q, %v", data, readErr)
				}
				if !exists && !os.IsNotExist(readErr) {
					t.Fatalf("preview created file: %v", readErr)
				}
				entries, err := os.ReadDir(directory)
				wantEntries := 0
				if exists {
					wantEntries = 1
				}
				if err != nil || len(entries) != wantEntries {
					t.Fatalf("preview caused filesystem effects: %v, %v", entries, err)
				}
			})
		}
	}
}

func TestLiveDiffShellWriteRejectsUnsupportedShell(t *testing.T) {
	t.Parallel()
	for _, input := range []string{
		"cat source >file",
		"cat - >file <<'END'\nhello\nEND\n",
		"X=y cat >file <<'END'\nhello\nEND\n",
		"cat >file <<END\n$HOME\nEND\n",
		"cat >file <<END\n$(touch marker)\nEND\n",
		"cat >file <<END\n`touch marker`\nEND\n",
		"cat >\"$HOME/file\" <<'END'\nhello\nEND\n",
		"cat >file 3<<'END'\nhello\nEND\n",
		"cat >file >other <<'END'\nhello\nEND\n",
		"cat 2>file <<'END'\nhello\nEND\n",
		"cat >file <<'END'\nhello\nEND\necho after\n",
		"cat >file <<'END' | tee other\nhello\nEND\n",
		"cat >file <<'END' && echo after\nhello\nEND\n",
	} {
		t.Run(input, func(t *testing.T) {
			directory := t.TempDir()
			files, decoded, err := testLiveDiffShellWrite(t.Context(), input, directory)
			if decoded || len(files) != 0 || err != nil {
				t.Fatalf("unsupported input projected = %+v, %t, %v", files, decoded, err)
			}
			entries, err := os.ReadDir(directory)
			if err != nil || len(entries) != 0 {
				t.Fatalf("unsupported input had effects: %v, %v", entries, err)
			}
		})
	}
}

func TestLiveDiffShellWriteProjectionErrorsRemainDecoded(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"directory", "invalid UTF8"} {
		t.Run(kind, func(t *testing.T) {
			directory := t.TempDir()
			path := filepath.Join(directory, "file")
			var err error
			if kind == "directory" {
				err = os.Mkdir(path, 0o700)
			} else {
				err = os.WriteFile(path, []byte{0xff}, 0o600)
			}
			if err != nil {
				t.Fatal(err)
			}
			files, decoded, err := testLiveDiffShellWrite(t.Context(), "cat >>file <<'END'\nhello\nEND\n", directory)
			if !decoded || err == nil || len(files) != 0 {
				t.Fatalf("projection error = %+v, %t, %v", files, decoded, err)
			}
		})
	}
}

func TestLiveDiffPreviewWorkerCatFragmentsRetainLastDiff(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	broker, sub, worker := newLiveDiffWorkerTest(t, workspace)
	for _, delta := range []string{"cat >file.txt <<'END'\nfirst\n", "second\n", "END\n"} {
		worker.appendDelta(delta)
		// Paced frames may show a prefix of the delta first.
		line := strings.TrimPrefix(delta, "cat >file.txt <<'END'")
		preview := waitLiveDiffWorkerPreview(t, broker, sub, func(preview liveDiffPreview) bool {
			return preview.Status == liveDiffPreviewEdit && len(preview.Files) == 1 &&
				(line == "END\n" || strings.Contains(preview.Files[0].Diff, "+"+strings.TrimSpace(line)))
		})
		if preview.Input != "" || !strings.Contains(preview.Files[0].Diff, "+first") {
			t.Fatalf("cat source shown instead of diff: %+v", preview)
		}
	}
	worker.appendDelta("echo after\n")
	preview := waitLiveDiffWorkerPreview(t, broker, sub, func(preview liveDiffPreview) bool {
		return preview.Status == liveDiffPreviewEdit && len(preview.Files) == 1
	})
	if len(preview.Files) != 1 || !strings.Contains(preview.Files[0].Diff, "+second") || preview.Input != "" {
		t.Fatalf("unsupported suffix lost last diff: %+v", preview)
	}
	if _, err := os.Stat(filepath.Join(workspace, "file.txt")); !os.IsNotExist(err) {
		t.Fatalf("worker wrote file: %v", err)
	}
}

package router

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLiveDiffInterpreterWriteLiteralWrites(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, command, path, before, after string
	}{
		{
			name:    "Python Path.write_text",
			command: "python3 - <<'PY'\nfrom pathlib import Path\nPath(\"new.txt\").write_text(\"new content\\n\")\nPY\n",
			path:    "new.txt",
			after:   "new content\n",
		},
		{
			name:    "Python open write",
			command: "python3 - <<'PY'\nopen(\"existing.txt\", \"w\").write(\"replacement\\n\")\nPY\n",
			path:    "existing.txt",
			before:  "old content\n",
			after:   "replacement\n",
		},
		{
			name:    "Python read_text replace once",
			command: "python3 - <<'PY'\nfrom pathlib import Path\npath = Path(\"existing.txt\")\npath.write_text(path.read_text().replace(\"old\", \"new\", 1))\nPY\n",
			path:    "existing.txt",
			before:  "old old\n",
			after:   "new old\n",
		},
		{
			name:    "JavaScript fs.writeFileSync",
			command: "node - <<'JS'\nconst fs = require(\"node:fs\");\nfs.writeFileSync(\"new.txt\", \"new content\\n\");\nJS\n",
			path:    "new.txt",
			after:   "new content\n",
		},
		{
			name:    "JavaScript fs.writeFileSync assigned path",
			command: "node - <<'JS'\nconst fs = require(\"node:fs\");\nconst target = \"new.txt\";\nfs.writeFileSync(target, \"new content\\n\");\nJS\n",
			path:    "new.txt",
			after:   "new content\n",
		},
		{
			name:    "JavaScript promises writeFile",
			command: "node - <<'JS'\nconst fs = require(\"node:fs/promises\");\nawait fs.writeFile(\"existing.txt\", \"replacement\\n\");\nJS\n",
			path:    "existing.txt",
			before:  "old content\n",
			after:   "replacement\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			directory := t.TempDir()
			path := filepath.Join(directory, tc.path)
			if tc.before != "" {
				if err := os.WriteFile(path, []byte(tc.before), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			files, recognized, err := liveDiffInterpreterWrite(t.Context(), mustShellStatement(t, tc.command), directory, false)
			if err != nil || !recognized || len(files) != 1 {
				t.Fatalf("prediction = %+v, %t, %v; want one literal-write diff", files, recognized, err)
			}
			file := files[0]
			beforePath := path
			if tc.before == "" {
				beforePath = ""
			}
			if file.BeforePath != beforePath || file.AfterPath != path {
				t.Fatalf("predicted paths = %q -> %q, want %q -> %q", file.BeforePath, file.AfterPath, beforePath, path)
			}
			if !strings.Contains(file.Diff, "+"+strings.TrimSuffix(tc.after, "\n")) {
				t.Errorf("diff %q does not add %q", file.Diff, tc.after)
			}
			if tc.before != "" && !strings.Contains(file.Diff, "-"+strings.TrimSuffix(tc.before, "\n")) {
				t.Errorf("diff %q does not remove original content %q", file.Diff, tc.before)
			}

			got, readErr := os.ReadFile(path)
			if tc.before == "" {
				if !os.IsNotExist(readErr) {
					t.Fatalf("prediction created %q: read %q, %v", path, got, readErr)
				}
			} else if readErr != nil || string(got) != tc.before {
				t.Fatalf("prediction changed %q: got %q, %v; want original %q", path, got, readErr, tc.before)
			}
			entries, readDirErr := os.ReadDir(directory)
			wantEntries := 0
			if tc.before != "" {
				wantEntries = 1
			}
			if readDirErr != nil || len(entries) != wantEntries {
				t.Fatalf("prediction caused filesystem effects: entries=%v err=%v", entries, readDirErr)
			}
		})
	}
}

func TestLiveDiffInterpreterWriteDynamicBodiesFallBack(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, command, path string
	}{
		{
			name:    "Python dynamic body",
			command: "python3 - <<'PY'\nimport os\nfrom pathlib import Path\nPath(\"target.txt\").write_text(os.environ[\"LIVE_DIFF_BODY\"])\nPY\n",
			path:    "target.txt",
		},
		{
			name:    "JavaScript dynamic body",
			command: "node - <<'JS'\nconst fs = require(\"node:fs\");\nfs.writeFileSync(\"target.txt\", process.env.LIVE_DIFF_BODY);\nJS\n",
			path:    "target.txt",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			directory := t.TempDir()
			path := filepath.Join(directory, tc.path)
			const original = "existing content\n"
			if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
				t.Fatal(err)
			}

			files, recognized, err := liveDiffInterpreterWrite(t.Context(), mustShellStatement(t, tc.command), directory, false)
			if err != nil || recognized || len(files) != 0 {
				t.Fatalf("dynamic body prediction = %+v, %t, %v; want fallback without a guessed diff", files, recognized, err)
			}
			got, readErr := os.ReadFile(path)
			if readErr != nil || string(got) != original {
				t.Fatalf("dynamic prediction changed target: got %q, %v", got, readErr)
			}
		})
	}
}

func TestLiveDiffInterpreterWriteSkipsConditionalAndFunctionBodies(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, command string
	}{
		{
			name:    "Python conditional and function",
			command: "python3 - <<'PY'\nfrom pathlib import Path\nif True:\n    Path(\"conditional.txt\").write_text(\"conditional\\n\")\ndef update():\n    Path(\"function.txt\").write_text(\"function\\n\")\nPY\n",
		},
		{
			name:    "JavaScript conditional and function",
			command: "node - <<'JS'\nconst fs = require(\"node:fs\");\nif (true) { fs.writeFileSync(\"conditional.txt\", \"conditional\\n\"); }\nfunction update() { fs.writeFileSync(\"function.txt\", \"function\\n\"); }\nJS\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			directory := t.TempDir()
			files, recognized, err := liveDiffInterpreterWrite(t.Context(), mustShellStatement(t, tc.command), directory, false)
			if err != nil || recognized || len(files) != 0 {
				t.Fatalf("conditional/function prediction = %+v, %t, %v; want no predicted files", files, recognized, err)
			}
			entries, readDirErr := os.ReadDir(directory)
			if readDirErr != nil || len(entries) != 0 {
				t.Fatalf("conditional/function preview caused filesystem effects: entries=%v err=%v", entries, readDirErr)
			}
		})
	}
}

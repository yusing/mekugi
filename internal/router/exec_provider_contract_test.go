package router

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/libraries/commandargs"

	"github.com/yusing/mekugi/libraries/commandanalysis"

	"github.com/yusing/mekugi/internal/execscope"
)

func TestExecGitScopeRejectsRevisionOptionInjection(t *testing.T) {
	root := newExecVCSTestRepo(t, map[string]string{"tracked": "old\n"})
	marker := filepath.Join(root, "query-output")
	plan := execscope.Classify(execscope.Input{Command: "git restore --source=" + commandargs.QuoteArgument("--output="+marker) + " -- tracked", Directory: root, Shell: "bash", Deadline: time.Now().Add(execscope.Budget)})
	if !strings.Contains(plan.Reason, "query option") {
		t.Fatalf("unsafe revision accepted: %+v", plan)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("scope query wrote output: %v", err)
	}
}

func TestExecProviderAliasesRecursionAndPreviewPermissions(t *testing.T) {
	for _, test := range []struct{ name, command, target string }{
		{"python alias", `python3 -c 'from pathlib import Path as P; p = P("a"); p.write_text("new")'`, "a"},
		{"python subprocess", `python3 -c 'import subprocess; subprocess.run(["sh", "-c", "printf new > a"])'`, "a"},
		{"js import alias", `node --input-type=module -e 'import {writeFileSync as save} from "node:fs"; save("a", "new")'`, "a"},
		{"js destructured alias", `node -e 'const {writeFileSync: save}=require("node:fs"); save("a", "new")'`, "a"},
		{"node permission", `node --permission --allow-fs-write=src -e 'unknown()'`, "src/a"},
		{"deno permission", `deno run --allow-write=src edit.ts`, "src/a"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, filepath.Join(root, test.target), "before\n")
			writeTestFile(t, filepath.Join(root, "edit.ts"), "unknown();\n")
			observation, ok := captureExecObservation([]commandanalysis.Command{{Command: test.command, Workdir: root, Shell: "bash"}}, false, false, execCaptureEnv{directory: root, previewOnly: strings.HasSuffix(test.name, "permission")})
			if !ok || observation.Class != execscope.Scoped.String() {
				t.Fatalf("scope: %+v", observation)
			}
			captured := false
			for _, file := range observation.Files {
				captured = captured || file.Path == filepath.Join(root, test.target) && file.Content == "before\n"
			}
			if !captured {
				t.Fatalf("target baseline missing: %+v", observation)
			}
		})
	}
}

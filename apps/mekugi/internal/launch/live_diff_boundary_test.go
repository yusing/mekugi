package launch

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/yusing/mekugi/modules/livepreview/projection"

	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"
	previewview "github.com/yusing/mekugi/modules/livepreview/terminalview"
)

func TestLiveDiffPreviewDeltaBoundaries(t *testing.T) {
	for _, kind := range []string{applyPatchToolName, nativeExecCommandToolName, "exec"} {
		t.Run(kind, func(t *testing.T) {
			workspace := t.TempDir()
			patch := "*** Begin Patch\n*** Add File: notes.md\n+complete line\n+unfinished tail"
			input := patch
			if kind == nativeExecCommandToolName {
				input = `{"cmd":` + strconv.Quote("cat > notes.md <<'EOF'\ncomplete line\nunfinished tail") + `}`
			} else if kind == "exec" {
				input = "await tools.apply_patch(" + strconv.Quote(patch) + ");"
			}
			previewContext := t.Context()
			previewProject := previewProjector(previewContext, kind)
			for split := 1; split <= len(input); split++ {
				// Include trailing envelope fields as the worker does once caught up.
				preview, _ := previewProject(input[:split], workspace, false)
				for _, file := range preview.Files {
					for row := range strings.SplitSeq(file.Diff, "\n") {
						if strings.HasPrefix(row, "+") && !strings.HasPrefix(row, "+++") && row != "+complete line" {
							t.Fatalf("split %d exposed a partial line: %q", split, file.Diff)
						}
					}
				}
			}
			preview, _ := previewProject(input, workspace, true)
			if len(preview.Files) != 1 || !strings.Contains(preview.Files[0].Diff, "+unfinished tail") {
				t.Fatalf("final unterminated content lost: %+v", preview)
			}
		})
	}
}

func liveDiffStatementBurstCases() []struct{ path, body, first, second string } {
	return []struct{ path, body, first, second string }{
		{"out.py", "python_options = {\n 'enabled': True,\n}\npython_count = 2\n", "python_options", "python_count"},
		{"out.py", "python_left = 1; python_right = 2\n", "python_left", "python_right"},
		{"out.js", "const jsLeft = 1; const jsRight = 2;\n", "jsLeft", "jsRight"},
		{"out.ts", "const tsLeft: number = 1; const tsRight: number = 2;\n", "tsLeft", "tsRight"},
		{"out.ts", "const tsOptions: Record<string, string> = {\n mode: 'fast; safe',\n};\nconst tsCount: number = 2;\n", "tsOptions", "tsCount"},
		{"out.go", "package demo\nvar goLeft = 1; var goRight = 2\n", "goLeft", "goRight"},
		{"out.go", "package demo\nvar goOptions = map[string]string{\n \"mode\": \"fast; safe\",\n}\nvar goReady = true\n", "goOptions", "goReady"},
		{"out.sh", "shell_left=1; shell_right=2\n", "shell_left", "shell_right"},
	}
}

func TestLiveDiffBurstRevealsStatementsSeparately(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{applyPatchToolName, "exec", nativeExecCommandToolName} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				workspace := t.TempDir()
				broker := newLivePreview(t.Context())
				setLiveScope(broker, previewmodel.Scope{Workspaces: map[string]map[string]bool{workspace: {"root": true}}})
				sub := testReview(broker).Subscribe()
				defer testReview(broker).Unsubscribe(sub)
				<-sub.Events()
				// Share transport setup across the source forms. Each call still
				// receives both statements in one burst; controlled time exercises
				// real worker pacing without repeated wall-clock waits.
				sources := liveDiffStatementBurstCases()
				if kind != applyPatchToolName {
					// Language gating is shared. Encoded transports need the
					// multiline and same-line boundaries, not every language again.
					sources = sources[:2]
				}
				for _, source := range sources {
					input := "*** Begin Patch\n*** Add File: " + source.path + "\n+" + strings.ReplaceAll(strings.TrimSuffix(source.body, "\n"), "\n", "\n+") + "\n"
					if kind == "exec" {
						input = "await tools.apply_patch(" + strconv.Quote(input)
					} else if kind == nativeExecCommandToolName {
						command := "python3 - <<'PY'\nfrom pathlib import Path\nPath(" + strconv.Quote(source.path) + ").write_text(" + strconv.Quote(source.body) + ")\n"
						input = `{"cmd":` + strconv.Quote(command)
					}
					worker := startLiveDiffPreview(t.Context(), broker, workspace, "root", kind)
					t.Cleanup(worker.Stop)
					worker.Append(input)
					first := waitLiveDiffWorkerPreview(t, broker, sub, func(preview previewmodel.Preview) bool {
						return len(preview.Files) == 1 && strings.Contains(preview.Files[0].Diff, source.first)
					})
					if strings.Contains(first.Files[0].Diff, source.second) {
						t.Fatalf("two statements emitted together:\n%s", first.Files[0].Diff)
					}
					firstSeen := time.Now()
					waitLiveDiffWorkerPreview(t, broker, sub, func(preview previewmodel.Preview) bool {
						return len(preview.Files) == 1 && strings.Contains(preview.Files[0].Diff, source.second)
					})
					if elapsed := time.Since(firstSeen); elapsed < previewview.PreviewUnitDelay-2*previewview.PreviewFrameDelay {
						t.Fatalf("next statement overlapped the preceding fade: %v", elapsed)
					}
					worker.Stop()
					<-worker.Done()
				}
			})
		})
	}
}

func TestLiveDiffSemicolonCandidatesRequireTargetStatements(t *testing.T) {
	for _, tc := range []struct{ path, prefix string }{
		{"out.js", "const text = 'quoted;"},
		{"out.js", "// commented;"},
		{"out.js", "for (let i = 0;"},
		{"out.ts", "for (let i = 0; i < 5;"},
		{"out.py", "text = 'quoted;"},
		{"out.py", "# commented;"},
		{"out.go", "var text = `quoted;"},
		{"out.go", "// commented;"},
		{"out.go", "for i := 0;"},
		{"out.go", "if value := read();"},
		{"out.sh", "text='quoted;"},
		{"out.sh", "# commented;"},
		{"out.sh", "text=$(printf hi;"},
		{"out.txt", "ordinary text;"},
	} {
		if projection.SourceReady(t.Context(), tc.path, tc.prefix) {
			t.Errorf("non-statement candidate released for %s: %q", tc.path, tc.prefix)
		}
	}
}

func TestLiveDiffInterpreterRevealsCompleteTargetLines(t *testing.T) {
	for _, command := range []string{
		"python3 - <<'PY'\nfrom pathlib import Path\nPath('out.txt').write_text(\"first\\nsecond\\n\")\n",
		"python3 - <<'PY'\nfrom pathlib import Path\nPath('out.txt').write_text('first\\nsecond\\n')\n",
		"python3 - <<'PY'\nfrom pathlib import Path\nPath('out.txt').write_text(\n'''first\nsecond\n''')\n",
		"node - <<'JS'\nconst fs = require('node:fs');\nfs.writeFileSync(\n'out.txt', `first\nsecond\n`);\n",
	} {
		previewContext := t.Context()
		previewProject := previewProjector(previewContext, "")
		workspace := t.TempDir()
		for split := strings.Index(command, "first"); split < len(command)-1; split++ {
			preview, _ := previewProject(command[:split], workspace, false)
			for _, file := range preview.Files {
				for row := range strings.SplitSeq(file.Diff, "\n") {
					if strings.HasPrefix(row, "+") && !strings.HasPrefix(row, "+++") && row != "+first" && row != "+second" {
						t.Fatalf("partial target line at %d: %q", split, file.Diff)
					}
				}
			}
		}
		first, _ := previewProject(command[:strings.Index(command, "second")], workspace, false)
		if len(first.Files) != 1 || !strings.Contains(first.Files[0].Diff, "+first") {
			t.Fatalf("target line waited for the interpreter statement to close: %+v", first)
		}
		preview, _ := previewProject(command, workspace, false)
		if len(preview.Files) != 1 || !strings.Contains(preview.Files[0].Diff, "+second") {
			t.Fatalf("complete statement was not revealed before call completion: %+v", preview)
		}
	}
}

func TestLiveDiffSourceStatementsAcrossTransports(t *testing.T) {
	for _, source := range []struct{ path, body string }{
		{"out.js", "const value = fn(\n  1,\n);\n"},
		{"out.ts", "const value: number = fn(\n  1,\n);\n"},
		{"out.tsx", "const view = <div>\n  hello\n</div>;\n"},
		{"out.py", "value = fn(\n  1,\n)\n"},
		{"out.go", "var value = fn(\n  1,\n)\n"},
		{"out.go", "var value = struct {\n  field int\n}{}\n"},
		{"out.sh", "echo $(\n  printf value\n)\n"},
	} {
		for _, kind := range []string{applyPatchToolName, nativeExecCommandToolName, "exec"} {
			t.Run(source.path+"/"+kind, func(t *testing.T) {
				workspace := t.TempDir()
				previewContext := t.Context()
				previewProject := previewProjector(previewContext, kind)
				encode := func(body string) string {
					if kind == applyPatchToolName {
						return "*** Begin Patch\n*** Add File: " + source.path + "\n+" + strings.ReplaceAll(strings.TrimSuffix(body, "\n"), "\n", "\n+") + "\n"
					}
					cmd := "cat >" + source.path + " <<'EOF'\n" + body
					if source.path == "out.py" && body == source.body {
						// Ordinary Python source stays hidden until it can no
						// longer be an edit script being composed.
						cmd += "EOF\n"
					}
					if kind == nativeExecCommandToolName {
						return `{"cmd":` + strconv.Quote(cmd) + `}`
					}
					return "await tools.exec_command({cmd:" + strconv.Quote(cmd) + "});"
				}
				for end := strings.IndexByte(source.body, '\n') + 1; end < len(source.body); end++ {
					if source.body[end-1] != '\n' {
						continue
					}
					input := encode(source.body[:end])
					preview, _ := previewProject(input, workspace, false)
					if len(preview.Files) != 0 {
						t.Fatalf("incomplete source statement displayed: %+v", preview.Files)
					}
					preview, _ = previewProject(input, workspace, true)
					if len(preview.Files) != 1 {
						t.Fatalf("final partial source lost: %+v", preview)
					}
				}
				preview, _ := previewProject(encode(source.body), workspace, false)
				if len(preview.Files) != 1 {
					t.Fatalf("complete source statement held: %+v", preview)
				}
			})
		}
	}
}

func TestLiveDiffEmptyHeredocRetainsCompletedCard(t *testing.T) {
	workspace := t.TempDir()
	previewContext := t.Context()
	previewProject := previewProjector(previewContext, "")
	previewPatch := previewProjector(previewContext, applyPatchToolName)
	for _, command := range []string{"cat >next.txt", "tee next.txt"} {
		var pane previewview.PreviewPane
		first, _ := previewPatch("*** Begin Patch\n*** Add File: first.txt\n+visible\n*** End Patch\n", workspace, true)
		first.ID, first.Workspace = "first", workspace
		pane.Update(first, time.Now())
		pending, _ := previewProject(command+" <<'EOF'\n", workspace, false)
		pending.ID, pending.Workspace = "next", workspace
		pane.Update(pending, time.Now())
		if len(pane.Order) != 1 || pane.Order[0] != first.ID {
			t.Fatalf("empty %s header replaced prior diff: %v", command, pane.Order)
		}
		complete, _ := previewProject(command+" <<'EOF'\nEOF\n", workspace, true)
		if len(complete.Files) != 1 {
			t.Fatalf("explicit empty %s creation lost: %+v", command, complete)
		}
	}
}

func TestLiveDiffBufferedStatementKeepsMultiFileSnapshot(t *testing.T) {
	for _, kind := range []string{applyPatchToolName, nativeExecCommandToolName} {
		t.Run(kind, func(t *testing.T) {
			workspace := t.TempDir()
			broker := newLivePreview(t.Context())
			setLiveScope(broker, previewmodel.Scope{Workspaces: map[string]map[string]bool{workspace: {"thread": true}}})
			sub := testReview(broker).Subscribe()
			<-sub.Events()
			previewContext := t.Context()
			previewProject := previewProjector(previewContext, kind)
			encode := func(tail string) string {
				if kind == applyPatchToolName {
					return "*** Begin Patch\n*** Add File: a.txt\n+first\n*** Add File: b.js\n+const x = 1;\n" + tail
				}
				return `{"cmd":` + strconv.Quote("cat >a.txt <<'EOF'\nfirst\nEOF\ncat >b.js <<'EOF'\nconst x = 1;\n"+strings.TrimPrefix(tail, "+")) + `}`
			}
			var pane previewview.PreviewPane
			for _, tail := range []string{"", "+const y = fn(\n"} {
				preview, _ := previewProject(encode(tail), workspace, false)
				preview.ID, preview.Workspace, preview.Thread = "call", workspace, "thread"
				broker.Update(preview, false)
				batch := testReview(broker).TakePreviews(sub)
				if len(batch) != 1 || batch[0].Preview == nil {
					t.Fatalf("missing snapshot: %+v", batch)
				}
				pane.Update(*batch[0].Preview, time.Now())
				current := pane.Views["call"].Current
				if len(current.Files) != 2 || !strings.Contains(current.Files[1].Diff, "+const x = 1;") || strings.Contains(current.Files[1].Diff, "+const y") {
					t.Fatalf("buffered statement erased earlier files or leaked: %+v", current.Files)
				}
			}
		})
	}
}

func TestLiveDiffStatementsInsideOpenBlocks(t *testing.T) {
	for _, tc := range []struct{ path, first, partial, complete string }{
		{"out.js", "function update() {\n  const one = 1;\n", "  const two = fn(\n", "  const two = fn(\n    2\n  );\n"},
		{"out.ts", "function update(): void {\n  const one = 1;\n", "  const two = fn(\n", "  const two = fn(\n    2\n  );\n"},
		{"out.go", "package p\nfunc update() {\n  one := 1\n", "  two := fn(\n", "  two := fn(\n    2,\n  )\n"},
		{"out.py", "def update():\n  one = 1\n", "  two = fn(\n", "  two = fn(\n    2\n  )\n"},
		{"out.sh", "update() {\n  one=1;\n", "  two=$(\n", "  two=$(\n    printf 2\n  );\n"},
		{"out.js", "class Update {\n  one() { return 1; }\n", "  two() {\n", "  two() { return 2; }\n"},
		{"out.ts", "class Update {\n  one: number = 1;\n", "  two: number = fn(\n", "  two: number = fn(\n    2\n  );\n"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			workspace := t.TempDir()
			previewContext := t.Context()
			previewProject := previewProjector(previewContext, applyPatchToolName)
			project := func(source string) previewmodel.Preview {
				input := "*** Begin Patch\n*** Add File: " + tc.path + "\n+" + strings.ReplaceAll(strings.TrimSuffix(source, "\n"), "\n", "\n+") + "\n"
				preview, _ := previewProject(input, workspace, false)
				return preview
			}
			if preview := project(tc.first); len(preview.Files) != 1 || !strings.Contains(preview.Files[0].Diff, "one") {
				t.Fatalf("first statement waited for closing block: %+v", preview)
			}
			if preview := project(tc.first + tc.partial); len(preview.Files) != 0 {
				t.Fatalf("partial statement leaked: %+v", preview)
			}
			if preview := project(tc.first + tc.complete); len(preview.Files) != 1 || !strings.Contains(preview.Files[0].Diff, "two") {
				t.Fatalf("next statement waited for closing block: %+v", preview)
			}
		})
	}
}

func TestLiveDiffStatementDelimitersInsideStrings(t *testing.T) {
	for _, tc := range []struct{ path, partial, complete string }{
		{"out.js", "const text = `first;\nsecond;\n", "const text = `first;\nsecond;\n`;\n"},
		{"out.ts", "const text: string = `first;\nsecond;\n", "const text: string = `first;\nsecond;\n`;\n"},
		{"out.py", "text = '''first;\nsecond;\n", "text = '''first;\nsecond;\n'''\n"},
		{"out.sh", "text='first;\nsecond;\n", "text='first;\nsecond;\n';\n"},
		{"out.go", "var text = `first;\nsecond;\n", "var text = `first;\nsecond;\n`\n"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			if projection.SourceComplete(t.Context(), tc.path, tc.partial) {
				t.Fatal("semicolon/newline inside unfinished string released a statement")
			}
			if !projection.SourceComplete(t.Context(), tc.path, tc.complete) {
				t.Fatal("complete string-bearing statement was buffered")
			}
		})
	}
}

func TestLiveDiffInterpreterReplacementPreview(t *testing.T) {
	python := func(body string) string {
		return "python3 - <<'PY'\nfrom pathlib import Path\n" + body
	}
	project := func(t *testing.T, workspace, command string, final bool) []string {
		t.Helper()
		previewContext := t.Context()
		previewProject := previewProjector(previewContext, nativeExecCommandToolName)
		preview, _ := previewProject(string(mustTestJSON(t, map[string]string{"cmd": command, "workdir": workspace})), workspace, final)
		var diffs []string
		for _, file := range preview.Files {
			diffs = append(diffs, file.Diff)
		}
		return diffs
	}
	t.Run("composed Path loop uses actual file baselines", func(t *testing.T) {
		workspace := t.TempDir()
		const before = "package fixture\n\nimport \"old/module\"\n"
		for _, name := range []string{"main", "worker"} {
			writeTestFile(t, filepath.Join(workspace, "pkg", name, "history.go"), before)
		}
		command := python("for name in ['main', 'worker']:\n p=Path('pkg')/name/'history.go';s=p.read_text().replace('old/module', 'new/module');p.write_text(s)\n")
		for _, final := range []bool{false, true} {
			input := command
			if final {
				input += "PY\n"
			}
			diffs := project(t, workspace, input, final)
			if len(diffs) != 2 {
				t.Fatalf("final=%v: composed paths lost independent edits: %q", final, diffs)
			}
			for _, diff := range diffs {
				if !strings.Contains(diff, "-import \"old/module\"") || !strings.Contains(diff, "+import \"new/module\"") {
					t.Fatalf("final=%v: replacement lost actual baseline: %s", final, diff)
				}
			}
		}
		for _, name := range []string{"main", "worker"} {
			if data, err := os.ReadFile(filepath.Join(workspace, "pkg", name, "history.go")); err != nil || string(data) != before {
				t.Fatalf("projection mutated baseline: %q %v", data, err)
			}
		}
	})
	t.Run("replacement closing a block is gated in its file", func(t *testing.T) {
		workspace := t.TempDir()
		writeTestFile(t, filepath.Join(workspace, "demo.go"), "package demo\n\nfunc launch() {\n\tif requested {\n\t\told()\n\t}\n\twait()\n}\n")
		next := "\t\tone()\n\t}\n\ttwo()\n\tthree()\n"
		command := python("p = Path('demo.go')\np.write_text(p.read_text().replace(" + strconv.Quote("\t\told()\n\t}\n\twait()\n") + ", " + strconv.Quote(next) + "))\nPY\n")
		cut := strings.Index(command, `three()\n`) + len(`three()\n`)
		if diffs := project(t, workspace, command[:cut], false); len(diffs) != 1 || !strings.Contains(diffs[0], "+\tthree()") {
			t.Fatalf("complete replacement lines waited for the interpreter call: %q", diffs)
		}
		if diffs := project(t, workspace, command[:cut-len(`)\n`)], false); len(diffs) != 0 {
			t.Fatalf("partial replacement statement leaked: %q", diffs)
		}
	})
	t.Run("completed replacement uses minimal hunks", func(t *testing.T) {
		workspace := t.TempDir()
		writeTestFile(t, filepath.Join(workspace, "notes.txt"), "foo\n"+strings.Repeat("same\n", 50)+"foo\n")
		diffs := project(t, workspace, python("p = Path('notes.txt')\np.write_text(p.read_text().replace('foo', 'bar'))\nPY\n"), true)
		if len(diffs) != 1 || strings.Count(diffs[0], "\n-foo") != 2 || strings.Count(diffs[0], "\n-same") != 0 {
			t.Fatalf("completed replacement inflated its diff: %q", diffs)
		}
	})
	t.Run("arriving overwrite keeps matched rows in place", func(t *testing.T) {
		workspace := t.TempDir()
		writeTestFile(t, filepath.Join(workspace, "out.txt"), "a\n}\n")
		command := python("Path('out.txt').write_text('b\\n}\\nc\\n')\nPY\n")
		for _, marker := range []string{`}\n`, `c\n`} {
			cut := strings.Index(command, marker) + len(marker)
			diffs := project(t, workspace, command[:cut], false)
			if len(diffs) != 1 || strings.Contains(diffs[0], "\n-}") || !strings.Contains(diffs[0], "\n }") {
				t.Fatalf("matched row moved while content arrives at %q: %q", marker, diffs)
			}
		}
	})
	t.Run("completed write is not gated by another file's language", func(t *testing.T) {
		workspace := t.TempDir()
		command := python("Path('a.go').write_text('package a\\n')\nPath('b.txt').write_text('x = f(\\nsecond\\n')\nPY\n")
		cut := strings.Index(command, `f(\n`) + len(`f(\n`)
		if diffs := project(t, workspace, command[:cut], false); len(diffs) != 2 || !strings.Contains(diffs[1], "+x = f(") {
			t.Fatalf("text target was gated as Go source: %q", diffs)
		}
	})
}

func TestLiveDiffFinishedCallDrainIsBounded(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		workspace := t.TempDir()
		broker := newLivePreview(t.Context())
		setLiveScope(broker, previewmodel.Scope{Workspaces: map[string]map[string]bool{workspace: {"root": true}}})
		sub := testReview(broker).Subscribe()
		defer testReview(broker).Unsubscribe(sub)
		<-sub.Events()
		var input strings.Builder
		input.WriteString("*** Begin Patch\n*** Add File: notes.md\n")
		for i := range 30 {
			input.WriteString("+line " + strconv.Itoa(i) + "\n")
		}
		input.WriteString("*** End Patch\n")
		worker := startLiveDiffPreview(t.Context(), broker, workspace, "root", applyPatchToolName)
		defer worker.Stop()
		worker.Append(input.String())
		finished := time.Now()
		worker.Finish(input.String())
		complete := waitLiveDiffWorkerPreview(t, broker, sub, func(preview previewmodel.Preview) bool { return preview.Complete })
		if elapsed := time.Since(finished); elapsed > previewview.PreviewFinishDrain+previewview.PreviewUnitDelay+4*previewview.PreviewFrameDelay {
			t.Fatalf("finished call drained queued units for %v", elapsed)
		}
		if len(complete.Files) != 1 || !strings.Contains(complete.Files[0].Diff, "+line 29") {
			t.Fatalf("completion lost final input: %+v", complete)
		}
	})
}

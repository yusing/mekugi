package launch

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"

	"github.com/yusing/mekugi/modules/changehistory/presentation"

	activitymodel "github.com/yusing/mekugi/modules/activity/feed"

	"github.com/yusing/mekugi/backends/codex/binding"

	"github.com/yusing/mekugi/libraries/commandargs"

	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

// Shell edit intent is only a requested operation. It is not a captured edit
// receipt and must never acquire applied line counts from command text.
func TestShellEditIntentClassification(t *testing.T) {
	for _, tc := range []struct {
		name, command string
		wantVerbs     []string
		wantPaths     []string
		wantProgram   string
		secret        string
		wantLanguage  string
	}{
		{
			name: "sed in-place", command: `sed -i 's/Answers\[0\]\.Id/Answers[0].ID/' internal/router/live_activity_view_test.go`,
			wantVerbs: []string{"Edit"}, wantPaths: []string{"internal/router/live_activity_view_test.go"}, wantProgram: "sed", secret: "Answers",
		},
		{
			name: "setup and in-place edit", command: "export ROOT=/tmp\nsed -i 's/PRIVATE_SED_SOURCE/new/' a.go",
			wantVerbs: []string{"Run", "Edit"}, wantPaths: []string{"a.go"}, wantProgram: "sed", secret: "PRIVATE_SED_SOURCE",
		},
		{
			name: "sed backup and tests", command: `/usr/bin/sed --in-place=.bak -e 's/PRIVATE_SED_SOURCE/new/' 'a file.go' b.go; go test ./...`,
			wantVerbs: []string{"Edit", "Edit", "Edit", "Edit", "Run"}, wantPaths: []string{"a file.go", "b.go", "a file.go.bak", "b.go.bak"}, wantProgram: "sed", secret: "PRIVATE_SED_SOURCE",
		},
		{
			name: "variable heredoc and neighbors", command: "tmpdir=$(mktemp -d)\nartifact=\"$tmpdir/findings.txt\"\ncat >\"$artifact\" <<'EOF'\nPRIVATE_CAT_SOURCE\nEOF\nprintf '%s\\n' \"$artifact\"",
			wantVerbs: []string{"Run", "Run", "Edit", "Run"}, wantPaths: []string{`"$artifact"`}, wantProgram: "cat", secret: "PRIVATE_CAT_SOURCE",
		},
		{
			name: "cat append and test", command: "cat >> a.go <<'EOF'\nPRIVATE_CAT_SOURCE\nEOF\ngo test ./internal/router",
			wantVerbs: []string{"Edit", "Run"}, wantPaths: []string{"a.go"}, wantProgram: "cat", secret: "PRIVATE_CAT_SOURCE",
		},
		{
			name: "cat same line heredoc neighbor", command: "cat >> a.go <<'EOF'; printf done\nPRIVATE_CAT_SOURCE\nEOF",
			wantVerbs: []string{"Edit", "Run"}, wantPaths: []string{"a.go"}, wantProgram: "cat", secret: "PRIVATE_CAT_SOURCE",
		},
		{
			name: "cat and test", command: "cat > a.go <<'EOF' && go test ./internal/router\nPRIVATE_CAT_SOURCE\nEOF",
			wantVerbs: []string{"Edit", "Run"}, wantPaths: []string{"a.go"}, wantProgram: "cat", secret: "PRIVATE_CAT_SOURCE",
		},
		{
			name: "python batch and test", command: "python3 - <<'PY'\nfrom pathlib import Path\na=Path('a.go'); a.write_text(a.read_text().replace('old', 'PRIVATE_PY_SOURCE'))\nb=Path('b.go'); b.write_text('new')\nPY\ngo test ./internal/router",
			wantVerbs: []string{"Edit", "Edit", "Run", "Run"}, wantPaths: []string{"a.go", "b.go"}, wantProgram: "python3", secret: "PRIVATE_PY_SOURCE", wantLanguage: "python",
		},
		{
			name: "python literal filename loop", command: "python3 - <<'PY'\nfrom pathlib import Path\nfor name in ['app_server_preview_test.go', 'app_server_events_test.go']:\n p=Path('internal/router')/name; s=p.read_text().replace('old','PRIVATE_LOOP_SOURCE'); p.write_text(s)\np=Path('internal/router/app_server_edit_receipts_test.go'); s=p.read_text().replace('old','new'); p.write_text(s)\nPY",
			wantVerbs: []string{"Edit", "Edit", "Edit", "Run"}, wantPaths: []string{"internal/router/app_server_preview_test.go", "internal/router/app_server_events_test.go", "internal/router/app_server_edit_receipts_test.go"}, wantProgram: "python3", secret: "PRIVATE_LOOP_SOURCE", wantLanguage: "python",
		},
		{
			name: "python mutated filename remains unresolved", command: "python3 - <<'PY'\nfrom pathlib import Path\nname='a'\nname += '.txt'\n(Path('src') / name).write_text('new')\nPY",
			wantVerbs: []string{"Run"}, wantProgram: "python3", secret: "src/a",
		},
		{
			name: "python named and unresolved targets", command: "python3 - <<'PY'\nfrom pathlib import Path\nPath('known.go').write_text('x')\nPath(dynamic).write_text('y')\nPY",
			wantVerbs: []string{"Edit", "Run"}, wantPaths: []string{"known.go"}, wantProgram: "python3",
		},
		{
			name: "pathlib open", command: `python3 -c 'from pathlib import Path; p=Path("a.go"); p.open("w").write("PRIVATE_PATH_SOURCE")'`,
			wantVerbs: []string{"Edit", "Run"}, wantPaths: []string{"a.go"}, wantProgram: "python3", secret: "PRIVATE_PATH_SOURCE", wantLanguage: "python",
		},
		{
			name: "node write", command: "node - <<'JS'\nconst fs = require('fs'); fs.writeFileSync('a.go', 'PRIVATE_JS_SOURCE')\nJS",
			wantVerbs: []string{"Edit", "Run"}, wantPaths: []string{"a.go"}, wantProgram: "node", secret: "PRIVATE_JS_SOURCE", wantLanguage: "javascript",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := toolActivityShell(tc.command)
			blocks := parseLiveActivity(sessionpresentation.ActivityEntry{Kind: "tool", Text: got})
			if len(blocks) != len(tc.wantVerbs) {
				t.Fatalf("blocks = %+v; text = %q", blocks, got)
			}
			for i, want := range tc.wantVerbs {
				if blocks[i].Verb != want {
					t.Errorf("block %d verb = %q, want %q: %q", i, blocks[i].Verb, want, got)
				}
			}
			for _, path := range tc.wantPaths {
				if !strings.Contains(got, "Edit `"+path+"` · "+tc.wantProgram+" (requested)") {
					t.Errorf("missing requested edit for %s: %q", path, got)
				}
			}
			if tc.wantLanguage != "" {
				found := false
				for _, block := range blocks {
					found = found || block.Verb == "Run" && block.Lang == tc.wantLanguage && strings.Contains(block.Code, tc.secret) && strings.Contains(block.Label, tc.wantProgram)
				}
				if !found {
					t.Errorf("missing inner program and interpreter attribution: %q", got)
				}
			} else if tc.secret != "" && strings.Contains(got, tc.secret) {
				t.Errorf("non-interpreter edit leaked source: %q", got)
			}
			if strings.Contains(got, "+1") || strings.Contains(got, "-1") {
				t.Errorf("requested edit implied applied stats: %q", got)
			}
		})
	}
}

func TestShellEditIntentKeepsUnknownHeredocNeighbor(t *testing.T) {
	command := "cat >> a.go <<'EDIT'; unknown-tool <<'UNKNOWN'\nPRIVATE_EDIT_SOURCE\nEDIT\nPRIVATE_UNKNOWN_BODY\nUNKNOWN"
	got := toolActivityShell(command)
	blocks := parseLiveActivity(sessionpresentation.ActivityEntry{Kind: "tool", Text: got})
	if len(blocks) != 2 || blocks[0].Verb != "Edit" || blocks[1].Verb != "Run" ||
		!strings.Contains(got, "Edit `a.go` · cat (requested)") ||
		strings.Contains(got, "PRIVATE_EDIT_SOURCE") || !strings.Contains(got, "PRIVATE_UNKNOWN_BODY") ||
		!strings.Contains(blocks[1].Code, "unknown-tool <<'UNKNOWN'") {
		t.Fatalf("mixed heredoc bodies were misattached: %q, blocks %+v", got, blocks)
	}
}

func TestShellMoveIntentOperands(t *testing.T) {
	for command, want := range map[string]string{
		`mv -T 'src/old name.go' src/new.go`: "Move `src/{old name.go=>new.go}` · mv (requested)",
		`mv -t dst src/a.go src/b.go`:        "Move `src/a.go => dst/a.go` · mv (requested)\n\nMove `src/b.go => dst/b.go` · mv (requested)",
	} {
		if got := toolActivityShell(command); got != want {
			t.Errorf("%s: got %q, want %q", command, got, want)
		}
	}
}

func TestCodeModeEditIntentBatchPreview(t *testing.T) {
	command := "python3 - <<'PY'\np='a.go';s=open(p).read().replace('before','PRIVATE_BATCH_SOURCE');open(p,'w').write(s)\np='b.go';s=open(p).read().replace('before','after');open(p,'w').write(s)\nPY\nenv -u BASH_ENV rtk go test ./internal/router -run 'TestEditIntent'"
	source := "const r = await tools.exec_command({cmd:" + string(responses.MustMarshalJSON(command)) + "}); text(r);"
	calls, ok := binding.ExecutionCalls(source, true)
	if !ok || len(calls) != 1 {
		t.Fatalf("exec call was not recognized: %+v", calls)
	}
	var arguments map[string]string
	if err := json.Unmarshal([]byte(responses.StringField(calls[0], "arguments")), &arguments); err != nil || arguments["cmd"] != command {
		t.Fatalf("exec call changed command: %+v, %v", arguments, err)
	}
	got := toolActivityShell(command)
	blocks := parseLiveActivity(sessionpresentation.ActivityEntry{Kind: "tool", Text: got})
	if len(blocks) != 4 || blocks[0].Verb != "Edit" || blocks[1].Verb != "Edit" || blocks[2].Verb != "Run" || blocks[3].Verb != "Run" ||
		!strings.Contains(got, "Edit `a.go` · python3 (requested)") ||
		!strings.Contains(got, "Edit `b.go` · python3 (requested)") ||
		!strings.Contains(blocks[3].Label, "go test ./internal/router") || blocks[2].Lang != "python" ||
		!strings.Contains(blocks[2].Code, "PRIVATE_BATCH_SOURCE") || strings.Contains(blocks[2].Code, "python3 -") {
		t.Fatalf("exec batch preview = %q, blocks %+v", got, blocks)
	}
}

func TestShellEditIntentDoesNotInventWrites(t *testing.T) {
	for _, command := range []string{
		`sed 's/old/new/' a.go`,
		`sed -n '/-i/p' a.go`,
		`sed -i "$SCRIPT" "$TARGET"`,
		`sed -i 's/old/new/' *.go`,
		`sed -i.bak 's/old/new/' *.go`,
		`mv "$SOURCE" target.go`,
		`mv *.new target/`,
		`mv --unknown source.go target.go`,
		`node -e 'process.stdout.write("hello")'`,
		`node -e 'document.open()'`,
		`python3 -c 'items=["x"];items.remove("x");print(items)'`,
		`python3 -c 'print("old".replace("old", "new"))'`,
		"unknown-tool <<'EOF'\nPRIVATE_UNKNOWN_SOURCE\nEOF",
		"python3 - <<'PY'\nprint('write_text and open(w) are words, not calls')\nPY",
		"node - <<'JS'\nconsole.log('writeFileSync is text')\nJS",
	} {
		got := toolActivityShell(command)
		if strings.Contains(got, "Edit ") || !strings.HasPrefix(got, "Run") {
			t.Errorf("read-only or unknown program projected as edit: %q", got)
		}
	}
}

func TestAppServerSedStartsAsEditBeforeReceipt(t *testing.T) {
	const command = `sed -i 's/Answers\[0\]\.Id/Answers[0].ID/' internal/router/live_activity_view_test.go`
	for _, thread := range []string{"main", "child"} {
		t.Run(thread, func(t *testing.T) {
			u := newAppServerSessionTestUI(t, t.TempDir())
			view := u.testActivity(u.shell.Transcript)
			if thread == "child" {
				appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": thread, "agentNickname": "worker"}})
				view = u.testActivity(u.shell.Agents)
			}
			item := binding.ThreadItem{ID: "sed-call", Type: "commandExecution", Command: command,
				CommandActions: []binding.CommandAction{{Type: "unknown", Command: command}}}
			for _, method := range []string{"item/started", "item/completed"} {
				appServerTestNotify(t, u, method, map[string]any{"threadId": thread, "turnId": "turn", "item": item})
				assertCapturedCommand(t, view, thread, "sed-call", "Edit", 0)
			}
			data := []*presentation.Receipt{{
				Thread: thread, Calls: []string{"sed-call"}, Text: "Edit `internal/router/live_activity_view_test.go` +1 -1 · sed",
			}}
			view.ApplyCapturedEdits(data)
			assertCapturedCommand(t, view, thread, "sed-call", "Edit", 0)
			if len(view.Entries) != 1 || !strings.Contains(view.Entries[0].Source.Text, "+1 -1") || strings.Contains(view.Entries[0].Source.Text, "requested") {
				t.Fatalf("receipt failed to replace requested edit in place: %+v", view.Entries)
			}
		})
	}
}

func TestAppServerEditIntentLiveAndRestored(t *testing.T) {
	const source = "mv b.go.new b.go\ncat > a.go <<'EOF'\nPRIVATE_NATIVE_SOURCE\nEOF\ngo test ./internal/router"
	command := "/usr/bin/bash -lc " + commandargs.QuoteArgument(source)
	item := binding.ThreadItem{ID: "cmd", Type: "commandExecution", Command: command,
		CommandActions: []binding.CommandAction{{Type: "unknown", Command: command}}}
	for _, thread := range []string{"main", "child"} {
		t.Run(thread, func(t *testing.T) {
			u := newAppServerSessionTestUI(t, t.TempDir())
			if thread == "child" {
				appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "child", "agentNickname": "worker"}})
			}
			view := u.testActivity(u.shell.Transcript)
			if thread == "child" {
				view = u.testActivity(u.shell.Agents)
			}
			for _, method := range []string{"item/started", "item/completed"} {
				current := item
				if method == "item/completed" {
					current.ExitCode = new(2)
				}
				appServerTestNotify(t, u, method, map[string]any{"threadId": thread, "turnId": "t", "item": current})
				assertCapturedCommand(t, view, thread, "cmd", "Move", 0)
				if method == "item/completed" {
					finishPacing(u.testActivity(u.shell.Transcript), u.testActivity(u.shell.Agents))
					if got := mainFeed(appServerTestFeatures(&appServerUI{activity: new(activitymodel.Service), shell: &terminalsession.Attachment{Transcript: view.View}, attached: attachedPresentation{executionPresentation: &executionPresentation{source: view.ActivitySource}}}), 100); !strings.Contains(got, "mv b.go.new b.go · exit 2") || strings.Contains(got, "PRIVATE_NATIVE_SOURCE") {
						t.Fatalf("batch failure missing: %s", got)
					}
				}
			}
			if item.ExitCode != nil || item.Command != command {
				t.Fatal("classification modified the native command item")
			}
			for _, entry := range view.Entries {
				if entry.Source.Native == nil || entry.Source.Native.Item != "cmd" {
					continue
				}
				if !strings.Contains(entry.Source.Text, "Edit `a.go` · cat (requested)") || strings.Contains(entry.Source.Text, "PRIVATE_NATIVE_SOURCE") || !strings.Contains(entry.Source.Text, "Run `go test ./internal/router`") {
					t.Fatalf("native edit intent display = %q", entry.Source.Text)
				}
			}
			if thread == "main" {
				restored := newAppServerSessionTestUI(t, t.TempDir())
				restored.restoreHistory([]binding.HistoryTurn{{ID: "t", Status: "completed", Items: []binding.ThreadItem{item}}})
				assertCapturedCommand(t, restored.testActivity(restored.shell.Transcript), "main", "cmd", "Move", 0)
			}
		})
	}
}

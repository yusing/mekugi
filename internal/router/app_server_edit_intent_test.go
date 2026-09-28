package router

import (
	"encoding/json"
	"strings"
	"testing"
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
	}{
		{
			name: "sed in-place", command: `sed -i 's/Answers\[0\]\.Id/Answers[0].ID/' internal/router/live_activity_view_test.go`,
			wantVerbs: []string{"Edit"}, wantPaths: []string{"internal/router/live_activity_view_test.go"}, wantProgram: "sed", secret: "Answers",
		},
		{
			name: "sed backup and tests", command: `/usr/bin/sed --in-place=.bak -e 's/PRIVATE_SED_SOURCE/new/' 'a file.go' b.go; go test ./...`,
			wantVerbs: []string{"Edit", "Edit", "Edit", "Edit", "Run"}, wantPaths: []string{"a file.go", "b.go", "a file.go.bak", "b.go.bak"}, wantProgram: "sed", secret: "PRIVATE_SED_SOURCE",
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
			wantVerbs: []string{"Edit", "Edit", "Run"}, wantPaths: []string{"a.go", "b.go"}, wantProgram: "python3", secret: "PRIVATE_PY_SOURCE",
		},
		{
			name: "python literal filename loop", command: "python3 - <<'PY'\nfrom pathlib import Path\nfor name in ['app_server_preview_test.go', 'app_server_events_test.go']:\n p=Path('internal/router')/name; s=p.read_text().replace('old','PRIVATE_LOOP_SOURCE'); p.write_text(s)\np=Path('internal/router/app_server_edit_receipts_test.go'); s=p.read_text().replace('old','new'); p.write_text(s)\nPY",
			wantVerbs: []string{"Edit", "Edit", "Edit"}, wantPaths: []string{"internal/router/app_server_preview_test.go", "internal/router/app_server_events_test.go", "internal/router/app_server_edit_receipts_test.go"}, wantProgram: "python3", secret: "PRIVATE_LOOP_SOURCE",
		},
		{
			name: "python mutated filename remains unresolved", command: "python3 - <<'PY'\nfrom pathlib import Path\nname='a'\nname += '.txt'\n(Path('src') / name).write_text('new')\nPY",
			wantVerbs: []string{"Edit"}, wantProgram: "python3", secret: "src/a",
		},
		{
			name: "pathlib open", command: `python3 -c 'from pathlib import Path; p=Path("a.go"); p.open("w").write("PRIVATE_PATH_SOURCE")'`,
			wantVerbs: []string{"Edit"}, wantPaths: []string{"a.go"}, wantProgram: "python3", secret: "PRIVATE_PATH_SOURCE",
		},
		{
			name: "node write", command: "node - <<'JS'\nconst fs = require('fs'); fs.writeFileSync('a.go', 'PRIVATE_JS_SOURCE')\nJS",
			wantVerbs: []string{"Edit"}, wantPaths: []string{"a.go"}, wantProgram: "node", secret: "PRIVATE_JS_SOURCE",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := toolActivityShell(tc.command)
			blocks := parseLiveActivity(activityPaneEntry{Kind: "tool", Text: got})
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
			if strings.Contains(got, tc.secret) || strings.Contains(got, "+1") || strings.Contains(got, "-1") {
				t.Errorf("requested edit leaked source or implied applied stats: %q", got)
			}
		})
	}
}

func TestShellEditIntentKeepsUnknownHeredocNeighbor(t *testing.T) {
	command := "cat >> a.go <<'EDIT'; unknown-tool <<'UNKNOWN'\nPRIVATE_EDIT_SOURCE\nEDIT\nPRIVATE_UNKNOWN_BODY\nUNKNOWN"
	got := toolActivityShell(command)
	blocks := parseLiveActivity(activityPaneEntry{Kind: "tool", Text: got})
	if len(blocks) != 2 || blocks[0].Verb != "Edit" || blocks[1].Verb != "Run" ||
		!strings.Contains(got, "Edit `a.go` · cat (requested)") ||
		strings.Contains(got, "PRIVATE_EDIT_SOURCE") || !strings.Contains(got, "PRIVATE_UNKNOWN_BODY") ||
		!strings.Contains(blocks[1].Code, "unknown-tool <<'UNKNOWN'") {
		t.Fatalf("mixed heredoc bodies were misattached: %q, blocks %+v", got, blocks)
	}
}

func TestCodeModeEditIntentBatchPreview(t *testing.T) {
	command := "python3 - <<'PY'\np='a.go';s=open(p).read().replace('before','PRIVATE_BATCH_SOURCE');open(p,'w').write(s)\np='b.go';s=open(p).read().replace('before','after');open(p,'w').write(s)\nPY\nenv -u BASH_ENV rtk go test ./internal/router -run 'TestEditIntent'"
	source := "const r = await tools.exec_command({cmd:" + string(mustMarshalJSON(command)) + "}); text(r);"
	calls, ok := toolActivityUnwrapExecCalls(source, true)
	if !ok || len(calls) != 1 {
		t.Fatalf("Code Mode call was not recognized: %+v", calls)
	}
	var arguments map[string]string
	if err := json.Unmarshal([]byte(jsonString(calls[0], "arguments")), &arguments); err != nil || arguments["cmd"] != command {
		t.Fatalf("Code Mode call changed command: %+v, %v", arguments, err)
	}
	got := toolActivityShell(command)
	blocks := parseLiveActivity(activityPaneEntry{Kind: "tool", Text: got})
	if len(blocks) != 3 || blocks[0].Verb != "Edit" || blocks[1].Verb != "Edit" || blocks[2].Verb != "Run" ||
		!strings.Contains(got, "Edit `a.go` · python3 (requested)") ||
		!strings.Contains(got, "Edit `b.go` · python3 (requested)") ||
		!strings.Contains(got, "go test ./internal/router") || strings.Contains(got, "PRIVATE_BATCH_SOURCE") {
		t.Fatalf("Code Mode batch preview = %q, blocks %+v", got, blocks)
	}
}

func TestLiveActivityRequestedEditReplacedByReceipt(t *testing.T) {
	view := newLiveActivityView()
	view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{
		Seq: 1, Agent: "/root/worker", Kind: "tool", CallID: "edit-call", Text: "Edit `a.go` · cat (requested)",
	}}})
	view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{
		Seq: 2, Agent: "/root/worker", Kind: "exit", CallID: "edit-call", Text: "2",
	}}})
	view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{
		Seq: 3, Agent: "/root/worker", Kind: "tool", CallID: "edit-call", Text: "Edit `a.go` +1 -1 · cat",
	}}})
	if len(view.entries) != 1 || view.entries[0].Seq != 1 || view.entries[0].Text != "Edit `a.go` +1 -1 · cat" ||
		len(view.blocks) != 1 || len(view.blocks[0]) != 1 || view.blocks[0][0].Verb != "Edit" || view.blocks[0][0].ExitCode != 2 {
		t.Fatalf("confirmed receipt did not replace requested edit and retain exit: entries %+v, blocks %+v", view.entries, view.blocks)
	}
}

func TestShellEditIntentDoesNotInventWrites(t *testing.T) {
	for _, command := range []string{
		`sed 's/old/new/' a.go`,
		`sed -n '/-i/p' a.go`,
		`sed -i "$SCRIPT" "$TARGET"`,
		`sed -i 's/old/new/' *.go`,
		`sed -i.bak 's/old/new/' *.go`,
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
			view := u.view
			if thread == "child" {
				appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": thread, "agentNickname": "worker"}})
				view = u.agents
			}
			item := appServerItem{ID: "sed-call", Type: "commandExecution", Command: command,
				CommandActions: []appServerCommandAction{{Type: "unknown", Command: command}}}
			for _, method := range []string{"item/started", "item/completed"} {
				appServerTestNotify(t, u, method, map[string]any{"threadId": thread, "turnId": "turn", "item": item})
				assertCapturedCommand(t, view, thread, "sed-call", "Edit", 0)
			}
			data := newLiveDiffData()
			data.order = []string{"sed-change"}
			data.attempts["sed-change"] = liveDiffAttempt{receipt: &capturedActivityEdit{
				thread: thread, calls: []string{"sed-call"}, text: "Edit `internal/router/live_activity_view_test.go` +1 -1 · sed",
			}}
			view.applyCapturedEdits(data)
			assertCapturedCommand(t, view, thread, "sed-call", "Edit", 0)
			if len(view.entries) != 1 || !strings.Contains(view.entries[0].Text, "+1 -1") || strings.Contains(view.entries[0].Text, "requested") {
				t.Fatalf("receipt failed to replace requested edit in place: %+v", view.entries)
			}
		})
	}
}

func TestAppServerEditIntentLiveAndRestored(t *testing.T) {
	const source = "cat > a.go <<'EOF'\nPRIVATE_NATIVE_SOURCE\nEOF\ngo test ./internal/router"
	command := "/usr/bin/bash -lc " + shellQuoteArgument(source)
	item := appServerItem{ID: "cmd", Type: "commandExecution", Command: command,
		CommandActions: []appServerCommandAction{{Type: "unknown", Command: command}}}
	for _, thread := range []string{"main", "child"} {
		t.Run(thread, func(t *testing.T) {
			u := newAppServerSessionTestUI(t, t.TempDir())
			if thread == "child" {
				appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "child", "agentNickname": "worker"}})
			}
			view := u.view
			if thread == "child" {
				view = u.agents
			}
			for _, method := range []string{"item/started", "item/completed"} {
				current := item
				wantExit := 0
				if method == "item/completed" {
					current.ExitCode = new(2)
					wantExit = 2
				}
				appServerTestNotify(t, u, method, map[string]any{"threadId": thread, "turnId": "t", "item": current})
				assertCapturedCommand(t, view, thread, "cmd", "Edit", wantExit)
			}
			if item.ExitCode != nil || item.Command != command {
				t.Fatal("classification modified the native command item")
			}
			for _, entry := range view.entries {
				if entry.native == nil || entry.native.item != "cmd" {
					continue
				}
				if !strings.Contains(entry.Text, "Edit `a.go` · cat (requested)") || strings.Contains(entry.Text, "PRIVATE_NATIVE_SOURCE") || !strings.Contains(entry.Text, "Run `go test ./internal/router`") {
					t.Fatalf("native edit intent display = %q", entry.Text)
				}
			}
			if thread == "main" {
				restored := newAppServerSessionTestUI(t, t.TempDir())
				restored.restoreHistory([]appServerHistoryTurn{{ID: "t", Status: "completed", Items: []appServerItem{item}}})
				assertCapturedCommand(t, restored.view, "main", "cmd", "Edit", 0)
			}
		})
	}
}

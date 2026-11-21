package sessionreview

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/activitydetail"
	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/libraries/legacycall"
	"github.com/yusing/mekugi/modules/changehistory/presentation"
)

func TestRewindChangesIncludesDescendantsAndRejectsGaps(t *testing.T) {
	dir := t.TempDir()
	sources := []binding.ThreadInfo{{ID: "main", Path: filepath.Join(dir, "main.jsonl")}, {ID: "child", Path: filepath.Join(dir, "child.jsonl")}}
	for _, source := range sources {
		text := fmt.Sprintf(`{"type":"session_meta","payload":{"id":%q,"cwd":%q}}`, source.ID, dir) + "\n"
		for i, name := range []string{"old", "selected"} {
			stamp := fmt.Sprintf("2026-10-21T01:00:0%dZ", i)
			text += fmt.Sprintf(`{"timestamp":%q,"type":"event_msg","payload":{"type":"task_started","turn_id":%q}}`, stamp, name) + "\n"
			text += fmt.Sprintf(`{"timestamp":%q,"type":"response_item","payload":{"type":"function_call","call_id":%q,"name":"exec_command","arguments":"{}"}}`, stamp, source.ID+"-"+name) + "\n"
			text += fmt.Sprintf(`{"timestamp":%q,"type":"response_item","payload":{"type":"function_call_output","call_id":%q,"output":"ok"}}`, stamp, source.ID+"-"+name) + "\n"
			text += fmt.Sprintf(`{"timestamp":%q,"type":"response_item","payload":{"type":"function_call","call_id":%q,"name":"exec_command","arguments":"{}"}}`, stamp, source.ID+"-"+name+"-more") + "\n"
			text += fmt.Sprintf(`{"timestamp":%q,"type":"response_item","payload":{"type":"function_call_output","call_id":%q,"output":"ok"}}`, stamp, source.ID+"-"+name+"-more") + "\n"
		}
		if err := os.WriteFile(source.Path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	build := func(mode string) *presentation.Snapshot {
		b := presentation.NewBuilder(nil)
		for i, item := range []string{"main-old", "child-old", "main-selected", "child-selected"} {
			thread, _, _ := strings.Cut(item, "-")
			id := fmt.Sprintf("amber%d", i+1)
			if mode == "mixed" && i == 0 {
				id = "amber3"
			}
			operation := item
			if mode == "missing" && i == 3 {
				operation = "missing"
			}
			workspace, path := dir, filepath.Join(dir, item)
			if mode == "overlapping-workspaces" && i == 3 {
				workspace, path = filepath.Join(dir, "nested"), filepath.Join(dir, "main-selected")
			}
			a := presentation.Attempt{Key: presentation.AttemptKey{Workspace: workspace, Call: item}, Change: id, Operation: activitydetail.Operation{Thread: thread, Item: operation}, Chunks: []presentation.Chunk{{CaptureOrder: uint64(i + 1), Review: diffmodel.ReviewFile{AfterPath: path}}}}
			if i == 0 && strings.HasPrefix(mode, "grouped-") {
				other := "main-old-more"
				if mode == "grouped-mixed" {
					other = "main-selected"
				}
				if mode == "grouped-missing" {
					other = "missing"
				}
				a.Receipt = CapturedReceipt(dir, legacycall.History{ExecutingThread: "main", ExecOutcome: &legacycall.Outcome{CodeMode: true}, HostResults: []legacycall.HostResult{{Tool: "exec_command", CallID: "main-old"}, {Tool: "exec_command", CallID: other}}}, nil)
			}
			if err := b.Add(a); err != nil {
				t.Fatal(err)
			}
		}
		// Diagnostic Git effects retain details but never select rewind writes,
		// even when their command timing is unavailable.
		diagnostic := presentation.Attempt{Key: presentation.AttemptKey{Workspace: dir, Call: "git"}, Change: "amber5", Operation: activitydetail.Operation{Thread: "main", Item: "missing-git"}, Chunks: []presentation.Chunk{{Review: diffmodel.ReviewFile{AfterPath: filepath.Join(dir, "imported"), Origin: "git reset"}}}}
		if err := b.Add(diagnostic); err != nil {
			t.Fatal(err)
		}
		return b.Finish()
	}
	for _, mode := range []string{"complete", "mixed", "missing", "wrong-source", "missing-turn", "straddling", "missing-end", "overlapping-workspaces", "grouped-same", "grouped-mixed", "grouped-missing"} {
		t.Run(mode, func(t *testing.T) {
			selectedSources := slices.Clone(sources)
			turn := "selected"
			if mode == "wrong-source" {
				selectedSources[1].Path = sources[0].Path
			}
			if mode == "missing-turn" {
				turn = "missing"
			}
			if mode == "straddling" || mode == "missing-end" {
				data, err := os.ReadFile(sources[1].Path)
				if err != nil {
					t.Fatal(err)
				}
				text := string(data)
				if mode == "straddling" {
					text += `{"timestamp":"2026-10-21T01:00:02Z","type":"response_item","payload":{"type":"function_call_output","call_id":"child-old","output":"late"}}` + "\n"
				} else {
					text = strings.Replace(text, `"type":"function_call_output","call_id":"child-old"`, `"type":"ignored","call_id":"child-old"`, 1)
				}
				selectedSources[1].Path = filepath.Join(t.TempDir(), "child.jsonl")
				if err := os.WriteFile(selectedSources[1].Path, []byte(text), 0600); err != nil {
					t.Fatal(err)
				}
			}
			changes, err := RewindChanges(t.Context(), build(mode), "main", turn, "", selectedSources)
			if mode != "complete" && mode != "grouped-same" {
				if err == nil || len(changes) != 0 {
					t.Fatalf("gap yielded changes: %+v, %v", changes, err)
				}
				return
			}
			if err != nil || len(changes) != 2 || changes[0].ID != "amber3" || changes[1].ID != "amber4" {
				t.Fatalf("selection: %+v, %v", changes, err)
			}
		})
	}
}

func TestRewindChangesMidTurnUserMessageBoundary(t *testing.T) {
	dir := t.TempDir()
	mainPath := filepath.Join(dir, "main.jsonl")
	childPath := filepath.Join(dir, "child.jsonl")
	mainRollout := strings.Join([]string{
		fmt.Sprintf(`{"type":"session_meta","payload":{"id":"main","cwd":%q}}`, dir),
		`{"timestamp":"2026-10-21T01:00:00Z","type":"event_msg","payload":{"type":"task_started","turn_id":"t1"}}`,
		`{"timestamp":"2026-10-21T01:00:01Z","type":"response_item","payload":{"type":"function_call","call_id":"before-steer","name":"exec_command","arguments":"{}"}}`,
		`{"timestamp":"2026-10-21T01:00:02Z","type":"response_item","payload":{"type":"function_call_output","call_id":"before-steer","output":"ok"}}`,
		`{"timestamp":"2026-10-21T01:00:05Z","type":"event_msg","payload":{"type":"item_completed","turn_id":"t1","item":{"type":"UserMessage","id":"steer-1"}}}`,
		`{"timestamp":"2026-10-21T01:00:06Z","type":"response_item","payload":{"type":"function_call","call_id":"after-steer","name":"exec_command","arguments":"{}"}}`,
		`{"timestamp":"2026-10-21T01:00:07Z","type":"response_item","payload":{"type":"function_call_output","call_id":"after-steer","output":"ok"}}`,
	}, "\n") + "\n"
	childRollout := strings.Join([]string{
		fmt.Sprintf(`{"type":"session_meta","payload":{"id":"child","cwd":%q}}`, dir),
		`{"timestamp":"2026-10-21T01:00:06Z","type":"response_item","payload":{"type":"function_call","call_id":"child-after-steer","name":"exec_command","arguments":"{}"}}`,
		`{"timestamp":"2026-10-21T01:00:07Z","type":"response_item","payload":{"type":"function_call_output","call_id":"child-after-steer","output":"ok"}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(mainPath, []byte(mainRollout), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(childPath, []byte(childRollout), 0600); err != nil {
		t.Fatal(err)
	}
	b := presentation.NewBuilder(nil)
	for i, spec := range []struct{ thread, call, id string }{
		{"main", "before-steer", "amber1"},
		{"main", "after-steer", "amber2"},
		{"child", "child-after-steer", "amber3"},
	} {
		a := presentation.Attempt{
			Key:       presentation.AttemptKey{Workspace: dir, Call: spec.call},
			Change:    spec.id,
			Operation: activitydetail.Operation{Thread: spec.thread, Item: spec.call},
			Chunks:    []presentation.Chunk{{CaptureOrder: uint64(i + 1), Review: diffmodel.ReviewFile{AfterPath: filepath.Join(dir, spec.call)}}},
		}
		if err := b.Add(a); err != nil {
			t.Fatal(err)
		}
	}
	snapshot := b.Finish()
	sources := []binding.ThreadInfo{{ID: "main", Path: mainPath}, {ID: "child", Path: childPath}}

	changes, err := RewindChanges(t.Context(), snapshot, "main", "t1", "steer-1", sources)
	if err != nil {
		t.Fatalf("mid-turn rewind changes: %v", err)
	}
	if len(changes) != 2 || changes[0].ID != "amber2" || changes[1].ID != "amber3" {
		t.Fatalf("expected only changes after steer-1, got %+v", changes)
	}

	if _, err := RewindChanges(t.Context(), snapshot, "main", "t1", "missing-steer", sources); err == nil {
		t.Fatal("expected error for missing mid-turn user message")
	}

	// A command already running when the mid-turn steer arrives straddles the boundary and is refused.
	straddlingPath := filepath.Join(dir, "main-straddling.jsonl")
	straddlingRollout := strings.Join([]string{
		fmt.Sprintf(`{"type":"session_meta","payload":{"id":"main","cwd":%q}}`, dir),
		`{"timestamp":"2026-10-21T01:00:00Z","type":"event_msg","payload":{"type":"task_started","turn_id":"t1"}}`,
		`{"timestamp":"2026-10-21T01:00:04Z","type":"response_item","payload":{"type":"function_call","call_id":"in-flight","name":"exec_command","arguments":"{}"}}`,
		`{"timestamp":"2026-10-21T01:00:05Z","type":"event_msg","payload":{"type":"item_completed","turn_id":"t1","item":{"type":"UserMessage","id":"steer-1"}}}`,
		`{"timestamp":"2026-10-21T01:00:06Z","type":"response_item","payload":{"type":"function_call_output","call_id":"in-flight","output":"ok"}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(straddlingPath, []byte(straddlingRollout), 0600); err != nil {
		t.Fatal(err)
	}
	sb := presentation.NewBuilder(nil)
	if err := sb.Add(presentation.Attempt{
		Key:       presentation.AttemptKey{Workspace: dir, Call: "in-flight"},
		Change:    "amber9",
		Operation: activitydetail.Operation{Thread: "main", Item: "in-flight"},
		Chunks:    []presentation.Chunk{{CaptureOrder: 1, Review: diffmodel.ReviewFile{AfterPath: filepath.Join(dir, "in-flight")}}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := RewindChanges(t.Context(), sb.Finish(), "main", "t1", "steer-1", []binding.ThreadInfo{{ID: "main", Path: straddlingPath}}); err == nil || !strings.Contains(err.Error(), "uncertain execution boundary") {
		t.Fatalf("expected uncertain execution boundary refusal for in-flight command, got %v", err)
	}
}

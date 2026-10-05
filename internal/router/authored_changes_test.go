package router

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yusing/mekugi"
)

func TestAuthoredChangesUnknownCommandsStayQuiet(t *testing.T) {
	for _, command := range []string{"npm install", "npm install > install.log", "go test ./...", "go generate ./...", "make assets", "custom-generator | tee output.js", "python3 -c 'print(1+1)' | tee output.js", "gofmt -w .", "python3 -c 'import os; open(os.environ[\"TARGET\"], \"w\").write(\"generated\")'"} {
		t.Run(command, func(t *testing.T) {
			workspace := t.TempDir()
			// Such commands are bounded only by a workspace snapshot.
			observation, observed := captureExecObservation([]execCommandInput{{Command: command, Workdir: workspace, Shell: "bash"}}, false, false, execCaptureEnv{directory: workspace})
			if observed || observation != nil && len(observation.Files) != 0 {
				t.Fatalf("unsupported output acquired review evidence: %+v", observation)
			}
		})
	}
	if observation, observed := captureExecObservation(nil, true, true, execCaptureEnv{directory: t.TempDir()}); observed || observation == nil || len(observation.Files) != 0 {
		t.Fatalf("dynamic exec call acquired named evidence or lost its classification: %+v", observation)
	}
}

func TestAuthoredChangesKnownEditsIgnoreIncidentalEffectsAndGitIgnore(t *testing.T) {
	for _, command := range []string{"cat > FIXME.md <<'EOF'\nauthored\nEOF", "sed -i 's/before/authored/' FIXME.md", "printf ignored | sed -i 's/before/authored/' FIXME.md", "python3 -c 'from pathlib import Path; Path(\"FIXME.md\").write_text(\"authored\\n\")'", "mv FIXME.md renamed.md", "rm FIXME.md"} {
		t.Run(command, func(t *testing.T) {
			f := newMChangesSliceFixture(t, "known-source")
			writeTestFile(t, filepath.Join(f.workspace, ".gitignore"), "FIXME.md\n")
			writeTestFile(t, filepath.Join(f.workspace, "FIXME.md"), "before\n")
			init := exec.Command("git", "init", "--quiet", f.workspace)
			if output, err := init.CombinedOutput(); err != nil {
				t.Fatalf("git init: %v: %s", err, output)
			}
			observation := observeTestCommand(t, f.workspace, command)
			host := exec.Command("bash", "-c", command)
			host.Dir = f.workspace
			if output, err := host.CombinedOutput(); err != nil {
				t.Fatalf("host: %v: %s", err, output)
			}
			writeTestFile(t, filepath.Join(f.workspace, "node_modules", "dependency.js"), strings.Repeat("generated\n", 1000))
			writeTestFile(t, filepath.Join(f.workspace, "arbitrary-output", "output.txt"), "incidental\n")
			proxy := &mekugiProxy{replayStore: f.store}
			history := mekugiHistory{ToolName: nativeExecCommandToolName, ExecutingThread: f.thread, ExecObservation: observation}
			if err := proxy.finalizeExecObservations(f.ctx, f.workspace, []execCompletion{{callID: "edit", history: history, output: mustMarshalJSON(map[string]any{"exit_code": 0})}}); err != nil {
				t.Fatal(err)
			}
			reopened, err := openMekugiReplayStore(f.store.directory)
			if err != nil {
				t.Fatal(err)
			}
			record, found, err := reopened.lookup(f.ctx, f.workspace, execDerivedCallID("edit", false))
			if err != nil || !found || record.ChangeID == "" || len(record.ReviewFiles) != 1 || record.ReviewFiles[0].Incomplete != "" {
				t.Fatalf("durable known edit: %+v, %v", record, err)
			}
			net, err := reopened.readChanges(f.ctx, changeReadOptions{workspace: f.workspace, ids: []string{record.ChangeID}, view: "net"})
			if err != nil || !strings.Contains(net, "FIXME.md") || strings.Contains(net, "generated") || strings.Contains(net, "incidental") {
				t.Fatalf("net acquired incidental output: %q, %v", net, err)
			}
			notice, err := reopened.agentEditNotice(f.ctx, f.workspace, "edit", history)
			if err != nil || !strings.Contains(notice, record.ChangeID) || strings.Contains(notice, "node_modules") {
				t.Fatalf("notice: %q, %v", notice, err)
			}
			if blobs := snapshotTestBlobs(t, reopened); len(blobs) != 0 {
				t.Fatalf("new edit produced snapshots: %v", blobs)
			}
		})
	}
}

func TestAuthoredChangesPipelinesKeepIndependentSourceEdits(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "FIXME.md")
	writeTestFile(t, path, "before\n")
	for _, command := range []string{
		"custom-generator | sed -i 's/before/authored/' FIXME.md",
		"printf authored > FIXME.md | custom-process",
		"custom-generator | printf authored > FIXME.md",
		"custom-generator | cat > FIXME.md <<'EOF'\nauthored\nEOF",
	} {
		observation := observeTestCommand(t, workspace, command)
		if len(observation.Files) != 1 || observation.Files[0].Path != path || observation.Files[0].Content != "before\n" {
			t.Fatalf("independent authored target lost: %+v", observation)
		}
	}
}

func TestAuthoredChangesLegacyObservationNoiseExcluded(t *testing.T) {
	f := newMChangesSliceFixture(t, "legacy-observer")
	id := f.reserve(t, f.thread, "window")
	path := filepath.Join(f.workspace, "random-generated-file")
	file := mekugi.RenderReviewFile("", path, "", strings.Repeat("generated\n", 1000))
	file.OriginNote = execInventoryNote
	f.publish(t, id, "window", execDerivedCallID("outer", false), mekugiHistory{ToolName: nativeExecCommandToolName, ExecOutcome: &execOutcome{Class: "opaque", Coverage: execCoveragePartial}, ReviewFiles: []mekugi.ReviewFile{file}})
	for _, view := range []string{"list", "summary", "", "net"} {
		text, err := f.store.readChanges(f.ctx, changeReadOptions{workspace: f.workspace, ids: []string{id}, view: view})
		if err != nil || strings.Contains(text, "generated") || strings.Contains(text, "incomplete captured scope") || strings.Contains(text, "1000") {
			t.Fatalf("legacy noise in %q: %q, %v", view, text, err)
		}
	}
	history, err := f.store.readChanges(f.ctx, changeReadOptions{workspace: f.workspace, ids: []string{id}, view: "history"})
	if err != nil || !strings.Contains(history, "generated") {
		t.Fatalf("diagnostic history lost: %q, %v", history, err)
	}
	notice, err := f.store.agentEditNotice(f.ctx, f.workspace, "outer", mekugiHistory{ExecObservation: &execObservation{}})
	if err != nil || notice != "" {
		t.Fatalf("observation-only notice: %q, %v", notice, err)
	}
	child := f.store.childJournalChanges(f.ctx, f.workspace, f.thread)
	if !strings.Contains(child, "No recorded changes") || strings.Contains(child, id) {
		t.Fatalf("observation-only handoff: %q", child)
	}
}

func TestAuthoredChangesInlineEvidenceSurvivesRestart(t *testing.T) {
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	history := snapshotTestHistory()
	if err := store.put(t.Context(), "/w", map[string]mekugiHistory{"inline": history}); err != nil {
		t.Fatal(err)
	}
	if blobs := snapshotTestBlobs(t, store); len(blobs) != 0 {
		t.Fatalf("new shared snapshots: %v", blobs)
	}
	data, err := os.ReadFile(filepath.Join(store.directory, replayRecordName("/w", "inline", false)))
	if err != nil || !strings.Contains(string(data), "before\\r\\n") {
		t.Fatalf("evidence not inline: %v", err)
	}
	reopened, err := openMekugiReplayStore(store.directory)
	if err != nil {
		t.Fatal(err)
	}
	got, found, err := reopened.lookup(t.Context(), "/w", "inline")
	if err != nil || !found || !reflect.DeepEqual(got, durableHistory(history)) {
		t.Fatalf("fresh reader evidence: %+v, %v", got, err)
	}
}

func TestAuthoredChangesPermissionRootsDoNotBecomeEditScope(t *testing.T) {
	workspace := t.TempDir()
	writeTestFile(t, filepath.Join(workspace, "output.js"), "generated\n")
	command := `node --permission --allow-fs-write=. -e 'require("unknown-generator")()'`
	if observation, observed := captureExecObservation([]execCommandInput{{Command: command, Workdir: workspace, Shell: "bash"}}, false, false, execCaptureEnv{directory: workspace}); observed || observation != nil && len(observation.Files) != 0 {
		t.Fatal("permissions acquired an authored tree scope")
	}
}

func TestAuthoredChangesNamedFormatterKeepsFinalSourceEdits(t *testing.T) {
	f := newMChangesSliceFixture(t, "named-formatter")
	path := filepath.Join(f.workspace, "chosen[fixture].md")
	writeTestFile(t, path, "package p; var A=1\n")
	writeTestFile(t, filepath.Join(f.workspace, "other.go"), "package other; var X=1\n")
	observation := observeTestCommand(t, f.workspace, "gofmt -w 'chosen[fixture].md'")
	if len(observation.Files) != 1 || observation.Files[0].Path != path || observation.Files[0].Origin != "" {
		t.Fatalf("formatter acquired unchosen or managed scope: %+v", observation)
	}
	host := exec.Command("gofmt", "-w", path)
	if output, err := host.CombinedOutput(); err != nil {
		t.Fatalf("gofmt: %v: %s", err, output)
	}
	proxy := &mekugiProxy{replayStore: f.store}
	history := mekugiHistory{ToolName: nativeExecCommandToolName, ExecutingThread: f.thread, ExecObservation: observation}
	if err := proxy.finalizeExecObservations(f.ctx, f.workspace, []execCompletion{{callID: "format", history: history, output: mustMarshalJSON(map[string]any{"exit_code": 0})}}); err != nil {
		t.Fatal(err)
	}
	record, found, err := f.store.lookup(f.ctx, f.workspace, execDerivedCallID("format", false))
	if err != nil || !found || record.ChangeID == "" {
		t.Fatalf("named formatter missing: %+v, %v", record, err)
	}
	net, err := f.store.readChanges(f.ctx, changeReadOptions{workspace: f.workspace, ids: []string{record.ChangeID}, view: "net"})
	if err != nil || !strings.Contains(net, "+var A = 1") || strings.Contains(net, "other.go") {
		t.Fatalf("final authored source not reviewable: %q, %v", net, err)
	}
}

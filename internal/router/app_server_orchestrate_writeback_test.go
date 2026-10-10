package router

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yusing/mekugi/internal/orchestrate"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

// Represent a crash after the first path effect but before its acknowledgement.
// Resume consumes the durable record through the real MCP entry point.
func retainShadowProgress(t *testing.T, s *orchestrate.Store, b orchestrate.Batch) {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(s.Directory, "*", "*.json"))
	if err != nil || len(paths) != 1 {
		t.Fatal(paths, err)
	}
	data, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]jsontext.Value
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	m["batches"], err = json.Marshal([]orchestrate.Batch{b})
	if err != nil {
		t.Fatal(err)
	}
	data, err = json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths[0], data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestOrchestrateShadowWritebackRecovery(t *testing.T) {
	workspace := t.TempDir()
	writeTestFile(t, filepath.Join(workspace, "first"), "base")
	writeTestFile(t, filepath.Join(workspace, "next"), "base")
	u := orchestrateCleanupUIInWorkspace(t, workspace)
	s, b := u.proxy.orchestration.store, u.orchestrateThreads["child"].batch
	gitTestRun(t, b.Cwd, "config", "user.name", "test")
	gitTestRun(t, b.Cwd, "config", "user.email", "test@example.invalid")
	for _, name := range []string{"first", "next"} {
		writeTestFile(t, filepath.Join(b.Cwd, name), "child")
	}
	gitTestCommit(t, b.Cwd)
	b, err := s.PlanShadowMerge(t.Context(), workspace, "main", "batch", "child")
	if err != nil {
		t.Fatal(err)
	}
	b.ShadowMerge.State = "applying"
	b.ShadowMerge.Writes = []orchestrate.ShadowWrite{{Path: "first", State: "applying"}, {Path: "next", State: "pending"}}
	retainShadowProgress(t, s, b)
	reopened := &orchestrate.Store{Directory: s.Directory, ShadowSnapshot: orchestrateShadowSnapshot}
	u.proxy.orchestration.store = reopened
	call := orchestrateTargetMCPClient(t, u, "integrate")
	call("main", true) // An uncertain preimage does not authorize replay.
	if got, _ := os.ReadFile(filepath.Join(workspace, "next")); string(got) != "base" {
		t.Fatal("uncertain effect repeated", string(got))
	}
	writeTestFile(t, filepath.Join(workspace, "first"), "child")
	writeTestFile(t, filepath.Join(workspace, "next"), "external")
	call("main", true) // Even a confirmed prefix cannot overwrite a changed preimage.
	if got, _ := os.ReadFile(filepath.Join(workspace, "next")); string(got) != "external" {
		t.Fatal("recovery overwrote external edit", string(got))
	}
	writeTestFile(t, filepath.Join(workspace, "next"), "base")
	writeTestFile(t, filepath.Join(workspace, "unrelated"), "keep")
	result := call("main", false)
	data, err := json.Marshal(result.StructuredContent)
	if err != nil || json.Unmarshal(data, &b) != nil || b.ShadowMerge.State != "applied" || b.Integration == nil {
		t.Fatal("recovery did not complete", result, err)
	}
	for name, want := range map[string]string{"first": "child", "next": "child", "unrelated": "keep"} {
		if got, err := os.ReadFile(filepath.Join(workspace, name)); err != nil || string(got) != want {
			t.Fatal(name, string(got), err)
		}
	}
	if _, err := (&orchestrate.Store{Directory: s.Directory}).RecordIntegration(t.Context(), workspace, "main", "batch", "child"); err != nil {
		t.Fatal("fresh owner lost proof", err)
	}
	// A follow-up child commit merges from the previously integrated child tip.
	writeTestFile(t, filepath.Join(b.Cwd, "first"), "child follow-up")
	gitTestCommit(t, b.Cwd)
	writeTestFile(t, filepath.Join(workspace, "first"), "external conflict")
	call("main", false)
	if u.orchestrateThreads["child"].batch.ShadowMerge.State != "conflicted" {
		t.Fatal("follow-up did not report conflict")
	}
	u.proxy.orchestration.store = &orchestrate.Store{Directory: s.Directory, ShadowSnapshot: orchestrateShadowSnapshot}
	writeTestFile(t, filepath.Join(workspace, "first"), "child")
	call("main", false)
	if got, _ := os.ReadFile(filepath.Join(workspace, "first")); string(got) != "child follow-up" {
		t.Fatal("follow-up conflicted with its own earlier result", string(got))
	}
}

func TestOrchestrateShadowWritebackPathPreservation(t *testing.T) {
	workspace := t.TempDir()
	writeTestFile(t, filepath.Join(workspace, "to_file", "deep", "leaf"), "base")
	writeTestFile(t, filepath.Join(workspace, "to_dir"), "base")
	writeTestFile(t, filepath.Join(workspace, ".gitignore"), "ignored/\nto_file/unknown\n")
	writeTestFile(t, filepath.Join(workspace, "ignored", "input"), "keep")
	writeTestFile(t, filepath.Join(workspace, "cache", ".svn", "entries"), "metadata")
	u := orchestrateCleanupUIInWorkspace(t, workspace)
	b := u.orchestrateThreads["child"].batch
	gitTestRun(t, b.Cwd, "config", "user.name", "test")
	gitTestRun(t, b.Cwd, "config", "user.email", "test@example.invalid")
	if err := os.Remove(filepath.Join(b.Cwd, "to_file", "deep", "leaf")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(b.Cwd, "to_file", "deep")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(b.Cwd, "to_file")); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(b.Cwd, "to_file"), "child file")
	if err := os.Remove(filepath.Join(b.Cwd, "to_dir")); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(b.Cwd, "to_dir", "deep", "leaf"), "child leaf")
	gitTestCommit(t, b.Cwd)
	call := orchestrateTargetMCPClient(t, u, "integrate")
	writeTestFile(t, filepath.Join(workspace, "to_file", "unknown"), "keep")
	call("main", true)
	if got, _ := os.ReadFile(filepath.Join(workspace, "to_dir")); string(got) != "base" {
		t.Fatal("unplanned content was detected after source writes", string(got))
	}
	if err := os.Remove(filepath.Join(workspace, "to_file", "unknown")); err != nil {
		t.Fatal(err)
	}
	call("main", false)
	for name, want := range map[string]string{"to_file": "child file", "to_dir/deep/leaf": "child leaf", "ignored/input": "keep", "cache/.svn/entries": "metadata"} {
		if got, err := os.ReadFile(filepath.Join(workspace, name)); err != nil || string(got) != want {
			t.Fatal(name, string(got), err)
		}
	}
	call("main", false)
	if _, err := u.proxy.orchestration.store.RecordIntegration(t.Context(), workspace, "main", "batch", "child"); err != nil {
		t.Fatal("transition proof failed", err)
	}
}

func TestOrchestrateShadowWritebackCancellationAndParentLinks(t *testing.T) {
	workspace := t.TempDir()
	writeTestFile(t, filepath.Join(workspace, "dir", "file"), "base")
	u := orchestrateCleanupUIInWorkspace(t, workspace)
	s, b := u.proxy.orchestration.store, u.orchestrateThreads["child"].batch
	gitTestRun(t, b.Cwd, "config", "user.name", "test")
	gitTestRun(t, b.Cwd, "config", "user.email", "test@example.invalid")
	writeTestFile(t, filepath.Join(b.Cwd, "dir", "file"), "child")
	gitTestCommit(t, b.Cwd)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.IntegrateShadow(ctx, workspace, "main", "batch", "child"); err == nil {
		t.Fatal("canceled integration succeeded")
	}
	if got, _ := os.ReadFile(filepath.Join(workspace, "dir", "file")); string(got) != "base" {
		t.Fatal("canceled call changed source", string(got))
	}
	b, err := s.PlanShadowMerge(t.Context(), workspace, "main", "batch", "child")
	if err != nil {
		t.Fatal(err)
	}
	b.ShadowMerge.State = "applying"
	b.ShadowMerge.Writes = []orchestrate.ShadowWrite{{Path: "dir/file", State: "pending"}}
	retainShadowProgress(t, s, b)
	external := t.TempDir()
	writeTestFile(t, filepath.Join(external, "file"), "base")
	if err := os.Rename(filepath.Join(workspace, "dir"), filepath.Join(workspace, "saved")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(workspace, "dir")); err != nil {
		t.Fatal(err)
	}
	result := orchestrateTargetMCPClient(t, u, "integrate")("main", true)
	if result.StructuredContent == nil {
		t.Fatal("partial result omitted durable progress")
	}
	if got, _ := os.ReadFile(filepath.Join(external, "file")); string(got) != "base" {
		t.Fatal("parent redirect wrote external bytes", string(got))
	}
	if err := os.Remove(filepath.Join(workspace, "dir")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(workspace, "saved"), filepath.Join(workspace, "dir")); err != nil {
		t.Fatal(err)
	}
	b.ShadowMerge.Writes[0].State = "applying"
	b.ShadowMerge.Writes[0].Temp = "dir/.mekugi-orchestrate-retained"
	retainShadowProgress(t, s, b)
	writeTestFile(t, filepath.Join(workspace, "dir", "file"), "child")
	writeTestFile(t, filepath.Join(workspace, b.ShadowMerge.Writes[0].Temp), "unknown")
	result = orchestrateTargetMCPClient(t, u, "integrate")("main", true)
	if !strings.Contains(result.Content[0].(*mcp.TextContent).Text, "temporary path") {
		t.Fatal("orphan temp was not reported", result)
	}
	if got, _ := os.ReadFile(filepath.Join(workspace, b.ShadowMerge.Writes[0].Temp)); string(got) != "unknown" {
		t.Fatal("uncertain temp was removed", string(got))
	}
}

func TestUISnapshotOrchestrationIntegrating(t *testing.T) {
	u := orchestrateCleanupUIInWorkspace(t, t.TempDir())
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	u.clock = func() time.Time { return now }
	u.agents.clock = u.clock
	u.status = "Ready"
	child := u.orchestrateThreads["child"]
	command := &orchestrateCommand{ctx: t.Context(), workspace: u.session.cwd, main: "main", target: "batch", planIntegration: true, reply: make(chan orchestrateResult, 1)}
	u.startOrchestratedChild(command)
	if !u.navigation.views["child"].orchestrateCheckoutUnavailable() || child.batch.State != "integrating" {
		t.Fatal("integration did not reserve child input before its background work")
	}
	child.batch.Branch = "mekugi/run/batch"
	u.orchestrationRoster()
	uisnapshot.Assert(t, "testdata/snapshots/orchestration-integrating-roster.txt", strings.Join(u.agents.nativeRoster(100, 12, now, true), "\n")+"\n")
	drainOrchestrateWork(t, u)
	if result := <-command.reply; result.err != nil || u.navigation.views["child"].orchestrateCheckoutUnavailable() {
		t.Fatal("integration failed to release input", result)
	}
}

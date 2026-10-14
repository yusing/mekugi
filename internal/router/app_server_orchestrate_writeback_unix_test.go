//go:build unix

package router

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/yusing/mekugi/internal/orchestrate"
)

func TestOrchestrateShadowWritebackNoEffectRecovery(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("requires filesystem permission enforcement")
	}
	workspace := t.TempDir()
	names := []string{"first", "locked/next"}
	for _, name := range names {
		writeTestFile(t, filepath.Join(workspace, name), "base")
	}
	u := orchestrateCleanupUIInWorkspace(t, workspace)
	s, b := u.proxy.orchestration.store, u.orchestrateThreads["child"].batch
	gitTestRun(t, b.Cwd, "config", "user.name", "test")
	gitTestRun(t, b.Cwd, "config", "user.email", "test@example.invalid")
	for _, name := range names {
		writeTestFile(t, filepath.Join(b.Cwd, name), "child")
	}
	gitTestCommit(t, b.Cwd)
	locked := filepath.Join(workspace, "locked")
	if err := os.Chmod(locked, 0555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0755) })
	call := orchestrateTargetMCPClient(t, u, "integrate")
	call("main", true)
	plan := u.orchestrateThreads["child"].batch.ShadowMerge
	last := plan.Writes[len(plan.Writes)-1]
	if last.State != "pending" || last.Temp != "" || plan.Writes[0].State != "applied" {
		t.Fatal("known no-effect failure lost recoverable progress", plan)
	}
	if err := os.Chmod(locked, 0755); err != nil {
		t.Fatal(err)
	}
	u.proxy.orchestration.store = &orchestrate.Store{Directory: s.Directory, ShadowSnapshot: orchestrateShadowSnapshot}
	call = orchestrateTargetMCPClient(t, u, "integrate")
	call("main", false)
	completed, err := s.Snapshot(workspace, "main")
	if err != nil || completed[0].Integration == nil {
		t.Fatal("restart did not recover", completed, err)
	}
	if got := completed[0].ShadowMerge; got.SourceTip != plan.SourceTip || got.Tree != plan.Tree || got.Tip != plan.Tip {
		t.Fatal("recovery replaced the retained source or child snapshot")
	}
	for _, name := range names {
		if got, err := os.ReadFile(filepath.Join(workspace, name)); err != nil || string(got) != "child" {
			t.Fatal(name, string(got), err)
		}
	}
}

func TestOrchestrateShadowWritebackExecutableUmask(t *testing.T) {
	const marker = "MEKUGI_TEST_SHADOW_UMASK"
	if os.Getenv(marker) != "1" {
		// Umask is process-wide; keep other tests outside this process.
		command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestOrchestrateShadowWritebackExecutableUmask$")
		command.Env = append(os.Environ(), marker+"=1")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("umask recovery: %v\n%s", err, output)
		}
		return
	}
	workspace := t.TempDir()
	writeTestFile(t, filepath.Join(workspace, "script"), "base")
	u := orchestrateCleanupUIInWorkspace(t, workspace)
	s, b := u.proxy.orchestration.store, u.orchestrateThreads["child"].batch
	gitTestRun(t, b.Cwd, "config", "user.name", "test")
	gitTestRun(t, b.Cwd, "config", "user.email", "test@example.invalid")
	writeTestFile(t, filepath.Join(b.Cwd, "script"), "child")
	if err := os.Chmod(filepath.Join(b.Cwd, "script"), 0755); err != nil {
		t.Fatal(err)
	}
	gitTestCommit(t, b.Cwd)
	b, err := s.PlanShadowMerge(t.Context(), workspace, "main", "batch", "child")
	if err != nil {
		t.Fatal(err)
	}
	b.ShadowMerge.State = "applying"
	b.ShadowMerge.Writes = []orchestrate.ShadowWrite{{Path: "script", State: "pending"}}
	retainShadowProgress(t, b)
	previous := syscall.Umask(0111)
	defer syscall.Umask(previous)
	orchestrateTargetMCPClient(t, u, "integrate")("main", false)
	if info, err := os.Stat(filepath.Join(workspace, "script")); err != nil || info.Mode().Perm()&0111 == 0 {
		t.Fatal("recovery lost executable state", info, err)
	}
	if _, err := s.RecordIntegration(t.Context(), workspace, "main", "batch", "child"); err != nil {
		t.Fatal("recovery returned unconfirmed source proof", err)
	}
}

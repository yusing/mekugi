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
	retainShadowProgress(t, s, b)
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

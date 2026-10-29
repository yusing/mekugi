package launch

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	threadbinding "github.com/yusing/mekugi/modules/threadcontrol/codexbinding"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

func claudeDiscoveryFixture(t *testing.T) (*threadExecution, *threadbinding.Driver, control.Identity, string) {
	t.Helper()
	if _, err := os.Stat("../../../../backends/claude/dist/worker.mjs"); err != nil {
		t.Skip("optional Claude SDK bundle has not been built")
	}
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("Bun is required for subprocess discovery checks")
	}
	fixture, err := filepath.Abs("../../../../backends/claude/testdata/mock-claude.mjs")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.Symlink(bun, filepath.Join(bin, "bun")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\nexec \"$MEKUGI_FIXTURE_BUN\" \"$MEKUGI_FIXTURE_CLI\" \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("MEKUGI_FIXTURE_BUN", bun)
	t.Setenv("MEKUGI_FIXTURE_CLI", fixture)
	t.Setenv("MEKUGI_FIXTURE_CAPTURED", "captured")
	report := filepath.Join(t.TempDir(), "initialization.json")
	t.Setenv("MEKUGI_FIXTURE_INITIALIZATION_REPORT", report)
	f := newClaudeHookFixture(t)
	e := f.bridge.execution
	e.setup = &threadSetup{environment: os.Environ()}
	t.Setenv("MEKUGI_FIXTURE_CAPTURED", "ambient")
	driver, err := threadbinding.NewDriver(e.shared)
	if err != nil {
		t.Fatal(err)
	}
	audience := sessioncontrol.NewAudience(nil)
	t.Cleanup(audience.Close)
	if err := driver.Connect(e, audience); err != nil {
		t.Fatal(err)
	}
	member, _ := e.shared.Member("parent")
	return e, driver, control.Identity{Thread: member.Thread, Session: member.Session, Turn: member.Turn, Call: "discovery", Item: "discovery"}, report
}

func TestClaudeModelDiscovery(t *testing.T) {
	e, driver, caller, report := claudeDiscoveryFixture(t)
	before, err := e.shared.Snapshot(caller.Thread)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := driver.Call(t.Context(), caller, "list_models", jsontext.Value(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Models []control.ModelOption `json:"models"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	want := []control.ModelOption{{ID: "claude:fixture-model", Name: "Fixture Model", Description: "Native fixture choice", ResolvedModel: "claude:fixture-resolved"}}
	if !reflect.DeepEqual(result.Models, want) || strings.Contains(string(raw), "private@example.invalid") {
		t.Fatalf("agent-facing model catalog = %s", raw)
	}
	data, err := os.ReadFile(report)
	if err != nil {
		t.Fatal(err)
	}
	var initialized struct {
		PID      int      `json:"pid"`
		CWD      string   `json:"cwd"`
		Args     []string `json:"args"`
		Captured string   `json:"captured"`
	}
	if err := json.Unmarshal(data, &initialized); err != nil {
		t.Fatal(err)
	}
	member, _ := e.shared.Member(caller.Thread)
	if initialized.CWD != member.Settings.Workspace || initialized.Captured != "captured" || !slices.Contains(initialized.Args, "--no-session-persistence") {
		t.Fatalf("discovery did not use admitted startup: %+v", initialized)
	}
	if !slices.Contains(initialized.Args, "--tools=") {
		t.Fatalf("native tools were not disabled: %v", initialized.Args)
	}
	if initialized.PID <= 0 || !errors.Is(syscall.Kill(initialized.PID, 0), syscall.ESRCH) {
		t.Fatalf("discovery native process %d was not joined", initialized.PID)
	}
	after, err := e.shared.Snapshot(caller.Thread)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("discovery changed logical membership/work: %v", err)
	}
}

func TestClaudeModelDiscoveryUnavailable(t *testing.T) {
	e, driver, caller, _ := claudeDiscoveryFixture(t)
	setup := e.setup
	e.setup = nil
	if _, err := driver.Call(t.Context(), caller, "list_models", jsontext.Value(`{}`)); err == nil || !strings.Contains(err.Error(), "captured invocation owners") {
		t.Fatalf("missing owners = %v", err)
	}
	e.setup = setup
	t.Setenv("PATH", t.TempDir())
	if _, err := driver.Call(t.Context(), caller, "list_models", jsontext.Value(`{}`)); err == nil || !strings.Contains(err.Error(), "Bun runtime") {
		t.Fatalf("missing runtime = %v", err)
	}
	if err := os.Symlink(os.Getenv("MEKUGI_FIXTURE_BUN"), filepath.Join(os.Getenv("PATH"), "bun")); err != nil {
		t.Fatal(err)
	}
	if _, err := driver.Call(t.Context(), caller, "list_models", jsontext.Value(`{}`)); err == nil || !strings.Contains(err.Error(), "Claude executable") {
		t.Fatalf("missing native executable = %v", err)
	}
	foreign := caller
	foreign.Session = "foreign"
	if _, err := e.DiscoverModels(t.Context(), foreign); err == nil || !strings.Contains(err.Error(), "foreign") {
		t.Fatalf("foreign caller = %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := driver.Call(ctx, caller, "list_models", jsontext.Value(`{}`)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled discovery = %v", err)
	}
}

func TestClaudeModelDiscoveryCancellationJoinsStartup(t *testing.T) {
	e, driver, caller, report := claudeDiscoveryFixture(t)
	e.setup.environment = append(e.setup.environment, "MEKUGI_FIXTURE_INIT_HANG=1")
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := driver.Call(ctx, caller, "list_models", jsontext.Value(`{}`))
		done <- err
	}()
	var initialized struct {
		PID int `json:"pid"`
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for initialized.PID == 0 {
		select {
		case <-ticker.C:
			if raw, err := os.ReadFile(report); err == nil {
				_ = json.Unmarshal(raw, &initialized)
			}
		case err := <-done:
			t.Fatalf("discovery ended before native initialization: %v", err)
		}
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("initialization cancellation = %v", err)
	}
	if !errors.Is(syscall.Kill(initialized.PID, 0), syscall.ESRCH) {
		t.Fatalf("cancelled discovery native process %d was not joined", initialized.PID)
	}
}

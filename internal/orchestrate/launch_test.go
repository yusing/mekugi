package orchestrate

import (
	"context"
	"encoding/json/jsontext"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/yusing/mekugi/internal/persistence"
)

func launchFixture(t *testing.T) (*Store, string, Batch) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-qm", "base"}} {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("fixture: %v: %s", err, out)
		}
	}
	s := &Store{Directory: t.TempDir()}
	b, err := s.Prepare(t.Context(), repo, "main", "batch")
	if err != nil {
		t.Fatal(err)
	}
	return s, repo, b
}

func TestLaunchRetainsIntentAndAcknowledgements(t *testing.T) {
	s, workspace, prepared := launchFixture(t)
	s.Writes = new(persistence.Counter)
	input := filepath.Join(prepared.Cwd, "input")
	if err := os.WriteFile(input, []byte("prepared input"), 0600); err != nil {
		t.Fatal(err)
	}
	params := jsontext.Value(`{ "permissions": ":workspace", "model": "model" }`)
	policy := jsontext.Value(`{"excludeSlashTmp":true,"networkAccess":true,"type":"workspaceWrite"}`)
	invocation := jsontext.Value(`{"agent_type":"custom-role","message":"handoff"}`)
	b, dispatch, err := s.BeginLaunch(t.Context(), workspace, "main", "batch", "handoff", params, policy, invocation)
	if err != nil || !dispatch || b.State != "prepared" || b.Launch == nil {
		t.Fatalf("reserve: %+v, %v, %v", b, dispatch, err)
	}
	s = &Store{Directory: s.Directory, Writes: s.Writes}
	for _, state := range []string{"starting", "started", "launched"} {
		before := s.Writes.Snapshot().Bytes
		b, dispatch, err = s.BeginLaunch(t.Context(), workspace, "main", "batch", "handoff", params, policy, invocation)
		if err != nil || dispatch || b.State != "prepared" || string(b.Launch.SandboxPolicy) != string(policy) {
			t.Fatalf("repeat %s: %+v, %v, %v", state, b, dispatch, err)
		}
		if s.Writes.Snapshot().Bytes != before {
			t.Fatal("repeat launch rewrote unchanged state")
		}
		switch state {
		case "starting":
			if err := s.RecordTurn(t.Context(), workspace, "main", "batch", "child", "turn"); err == nil {
				t.Fatal("turn accepted before thread publication")
			}
			for range 2 {
				if err := s.RecordThread(t.Context(), workspace, "main", "batch", "child"); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.RecordThread(t.Context(), workspace, "main", "batch", "other"); err == nil {
				t.Fatal("thread identity replaced")
			}
		case "started":
			for range 2 {
				if err := s.RecordTurn(t.Context(), workspace, "main", "batch", "child", "turn"); err != nil {
					t.Fatal(err)
				}
			}
		case "launched":
			if b.Launch.ThreadID != "child" || b.Launch.TurnID != "turn" {
				t.Fatalf("host identities lost: %+v", b.Launch)
			}
		}
	}
	if _, dispatch, err := s.BeginLaunch(t.Context(), workspace, "main", "batch", "changed", params, policy, invocation); err == nil || dispatch {
		t.Fatal("changed handoff accepted")
	}
	if _, dispatch, err := s.BeginLaunch(t.Context(), workspace, "main", "batch", "handoff", jsontext.Value(`{}`), policy, invocation); err == nil || dispatch {
		t.Fatal("changed configuration accepted")
	}
	if _, dispatch, err := s.BeginLaunch(t.Context(), workspace, "main", "batch", "handoff", params, nil, invocation); err == nil || dispatch {
		t.Fatal("changed first-turn policy accepted")
	}
	if _, dispatch, err := s.BeginLaunch(t.Context(), workspace, "main", "batch", "handoff", params, policy, jsontext.Value(`{"agent_type":"other-role","message":"handoff"}`)); err == nil || dispatch {
		t.Fatal("changed role accepted with identical resolved configuration")
	}
	if err := s.RecordTurn(t.Context(), workspace, "main", "batch", "child", "other"); err == nil {
		t.Fatal("turn identity replaced")
	}
	if data, err := os.ReadFile(input); err != nil || string(data) != "prepared input" {
		t.Fatalf("prepared input changed: %q, %v", data, err)
	}
}

func TestLaunchObservationSkipsUnchangedState(t *testing.T) {
	s, workspace, _ := launchFixture(t)
	if _, _, err := s.BeginLaunch(t.Context(), workspace, "main", "batch", "handoff", jsontext.Value(`{}`), nil, nil); err != nil {
		t.Fatal(err)
	}
	s.Writes = new(persistence.Counter)
	for _, observation := range [][2]string{{"running", ""}, {"failed", "host error"}, {"failed", "updated error"}, {"completed", ""}} {
		status, message := observation[0], observation[1]
		if err := s.ObserveLaunch(t.Context(), workspace, "main", "batch", status, message); err != nil {
			t.Fatal(err)
		}
		before := s.Writes.Snapshot().Bytes
		if err := s.ObserveLaunch(t.Context(), workspace, "main", "batch", status, message); err != nil {
			t.Fatal(err)
		}
		if s.Writes.Snapshot().Bytes != before {
			t.Fatal("unchanged host observation rewrote the manifest")
		}
		batches, err := s.Snapshot(workspace, "main")
		if err != nil || batches[0].Launch.HostStatus != status || batches[0].Error != message {
			t.Fatalf("changed host observation was not retained: %+v, %v", batches, err)
		}
	}
}

func TestLaunchCheckoutLocation(t *testing.T) {
	s, workspace, prepared := launchFixture(t)
	// Existing storage aliases are resolved at preparation, not rejected.
	alias := filepath.Join(t.TempDir(), "storage")
	if err := os.Symlink(s.Directory, alias); err != nil {
		t.Fatal(err)
	}
	s.Directory = alias
	aliased, err := s.Prepare(t.Context(), workspace, "main", "aliased")
	if err != nil {
		t.Fatal(err)
	}
	if err := validateLaunchCheckout(t.Context(), aliased); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Dir(prepared.Checkout)
	if err := os.Rename(parent, parent+"-moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(parent+"-moved", parent); err != nil {
		t.Fatal(err)
	}
	if _, dispatch, err := s.BeginLaunch(t.Context(), workspace, "main", "batch", "handoff", jsontext.Value(`{}`), nil, nil); err == nil || dispatch {
		t.Fatal("relocated checkout authorized dispatch")
	}
}

func TestLaunchRejectsChangedCheckoutAndCanceledReservation(t *testing.T) {
	s, workspace, prepared := launchFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, dispatch, err := s.BeginLaunch(ctx, workspace, "main", "batch", "handoff", jsontext.Value(`{}`), nil, nil); err == nil || dispatch {
		t.Fatal("canceled reservation authorized dispatch")
	}
	for i, change := range [][]string{
		{"checkout", "--detach", "--quiet"},
		{"checkout", "--quiet", prepared.Branch},
		{"-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-qm", "changed"},
	} {
		if _, err := git(t.Context(), prepared.Cwd, change...); err != nil {
			t.Fatal(err)
		}
		if i == 1 {
			continue // Restore the owned branch before changing its baseline.
		}
		if _, dispatch, err := s.BeginLaunch(t.Context(), workspace, "main", "batch", "handoff", jsontext.Value(`{}`), nil, nil); err == nil || dispatch {
			t.Fatalf("changed checkout authorized dispatch after %v", change)
		}
	}
	batches, err := s.List(t.Context(), workspace, "main")
	if err != nil || len(batches) != 1 || batches[0].State != "prepared" || batches[0].Launch != nil {
		t.Fatalf("rejected launch changed manifest: %+v, %v", batches, err)
	}
}

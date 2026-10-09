package router

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/yusing/mekugi/internal/orchestrate"
)

func TestAppServerOrchestrateThreadSettings(t *testing.T) {
	u, w := newAppServerTestUI()
	source, checkout, external := t.TempDir(), t.TempDir(), t.TempDir()
	u.session.cwd = filepath.Join(source, "src")
	if err := os.Mkdir(u.session.cwd, 0700); err != nil {
		t.Fatal(err)
	}
	batch := orchestrate.Batch{Checkout: checkout, Cwd: filepath.Join(checkout, "src")}
	u.model, u.reasoningEffort, u.serviceTier = "live-model", "high", "priority"
	// This is the status payload decoded from thread/start and thread/resume.
	if err := json.Unmarshal([]byte(`{"modelProvider":"mekugi_wrap","approvalPolicy":{"reject":{"sandbox_approval":true,"rules":false,"mcp_elicitations":false}},"approvalsReviewer":"auto_review","activePermissionProfile":{"id":"restricted","extends":":workspace"}}`), &u.statusConfig); err != nil {
		t.Fatal(err)
	}
	u.statusConfig.WorkspaceRoots = []string{source, filepath.Join(source, "src"), external}
	before, _ := json.Marshal(u.statusConfig)
	params, err := u.orchestrateThreadRequest(batch, orchestrateSpawnInput{})
	if err != nil {
		t.Fatal(err)
	}
	if err := u.request("thread/start", params); err != nil {
		t.Fatal(err)
	}
	assertAppServerSettingsRequest(t, w, "thread/start", map[string]any{
		"cwd": batch.Cwd, "runtimeWorkspaceRoots": []any{checkout, batch.Cwd, external},
		"model": "live-model", "modelProvider": "mekugi_wrap", "permissions": "restricted",
		"approvalPolicy": map[string]any{"reject": map[string]any{"sandbox_approval": true, "rules": false, "mcp_elicitations": false}}, "approvalsReviewer": "auto_review",
		"serviceTier": "priority", "config": map[string]any{"model_reasoning_effort": "high"},
	})
	// Explicit child budget changes do not change the coordinator.
	params, err = u.orchestrateThreadRequest(batch, orchestrateSpawnInput{Model: "child-model", ReasoningEffort: "low", ServiceTier: new("")})
	if err != nil {
		t.Fatal(err)
	}
	if params["model"] != "child-model" || params["serviceTier"] != nil || params["config"].(map[string]any)["model_reasoning_effort"] != "low" {
		t.Fatalf("override: %+v", params)
	}
	after, _ := json.Marshal(u.statusConfig)
	if string(before) != string(after) || u.model != "live-model" || u.reasoningEffort != "high" || u.serviceTier != "priority" {
		t.Fatal("child settings changed Main")
	}
	// Host updates supply current permission provenance, not just sandbox labels.
	appServerTestMessage(t, u, `{"method":"thread/settings/updated","params":{"threadId":"main","threadSettings":{"model":"live-model","effort":null,"serviceTier":null,"approvalPolicy":"on-request","approvalsReviewer":"user","activePermissionProfile":{"id":":read-only"},"sandboxPolicy":{"type":"read-only"}}}}`)
	u.models = []appServerModel{{Model: "live-model", DefaultEffort: "medium"}}
	params, err = u.orchestrateThreadRequest(batch, orchestrateSpawnInput{})
	if err != nil {
		t.Fatal(err)
	}
	if params["permissions"] != ":read-only" || params["approvalsReviewer"] != "user" || string(params["approvalPolicy"].(jsontext.Value)) != `"on-request"` || params["serviceTier"] != nil || params["config"].(map[string]any)["model_reasoning_effort"] != "medium" {
		t.Fatalf("live settings lost: %+v", params)
	}
	appServerTestMessage(t, u, `{"method":"thread/settings/updated","params":{"threadId":"other","threadSettings":{"activePermissionProfile":null}}}`)
	if _, err := u.orchestrateThreadRequest(batch, orchestrateSpawnInput{}); err != nil {
		t.Fatal("foreign thread cleared profile", err)
	}
	appServerTestMessage(t, u, `{"method":"thread/settings/updated","params":{"threadId":"main","threadSettings":{"activePermissionProfile":null}}}`)
	if _, err := u.orchestrateThreadRequest(batch, orchestrateSpawnInput{}); err == nil {
		t.Fatal("missing profile silently replaced")
	}
}

func TestAppServerOrchestrateWorkspaceRoots(t *testing.T) {
	source, checkout := t.TempDir(), t.TempDir()
	batch := orchestrate.Batch{Checkout: checkout, Cwd: checkout}
	roots, err := orchestrateWorkspaceRoots(source, batch, []string{})
	if err != nil || roots == nil || len(roots) != 0 {
		t.Fatalf("default roots: %v, %v", roots, err)
	}
	batch.Cwd = filepath.Join(filepath.Dir(checkout), "outside")
	if _, err := orchestrateWorkspaceRoots(source, batch, nil); err == nil {
		t.Fatal("outside cwd accepted")
	}
}

func TestAppServerOrchestrateAliasedWorkspace(t *testing.T) {
	repo := gitTestWorkspace(t)
	writeTestFile(t, filepath.Join(repo, "src", "file"), "base")
	gitTestCommit(t, repo)
	for _, sub := range []string{".", "src"} {
		t.Run(sub, func(t *testing.T) {
			selected := filepath.Join(repo, sub)
			alias := filepath.Join(t.TempDir(), "alias")
			if err := os.Symlink(selected, alias); err != nil {
				t.Fatal(err)
			}
			store := &orchestrate.Store{Directory: t.TempDir()}
			batch, err := store.Prepare(t.Context(), alias, "main", "batch")
			if err != nil {
				t.Fatal(err)
			}
			external := t.TempDir()
			roots, err := orchestrateWorkspaceRoots(alias, batch, []string{selected, alias, filepath.Join(alias, "future"), external})
			want := []string{batch.Cwd, filepath.Join(batch.Cwd, "future"), external}
			if err != nil || !reflect.DeepEqual(roots, want) {
				t.Fatalf("roots = %v, %v; want %v", roots, err, want)
			}
			link := filepath.Join(selected, "dangling")
			if err := os.Symlink(filepath.Join(external, "missing"), link); err != nil {
				t.Fatal(err)
			}
			if _, err := orchestrateWorkspaceRoots(alias, batch, []string{link}); err == nil {
				t.Fatal("dangling link was treated as a missing directory")
			}
		})
	}
}

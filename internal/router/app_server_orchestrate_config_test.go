package router

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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
	params, _, err := u.orchestrateThreadRequest(batch, orchestrateSpawnInput{})
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
	params, _, err = u.orchestrateThreadRequest(batch, orchestrateSpawnInput{Model: "child-model", ReasoningEffort: "low", ServiceTier: new("")})
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
	params, _, err = u.orchestrateThreadRequest(batch, orchestrateSpawnInput{})
	if err != nil {
		t.Fatal(err)
	}
	if params["permissions"] != ":read-only" || params["approvalsReviewer"] != "user" || string(params["approvalPolicy"].(jsontext.Value)) != `"on-request"` || params["serviceTier"] != nil || params["config"].(map[string]any)["model_reasoning_effort"] != "medium" {
		t.Fatalf("live settings lost: %+v", params)
	}
	appServerTestMessage(t, u, `{"method":"thread/settings/updated","params":{"threadId":"other","threadSettings":{"activePermissionProfile":null}}}`)
	if _, _, err := u.orchestrateThreadRequest(batch, orchestrateSpawnInput{}); err != nil {
		t.Fatal("foreign thread cleared profile", err)
	}
	appServerTestMessage(t, u, `{"method":"thread/settings/updated","params":{"threadId":"main","threadSettings":{"activePermissionProfile":null}}}`)
	if _, _, err := u.orchestrateThreadRequest(batch, orchestrateSpawnInput{}); err == nil {
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

func TestAppServerOrchestrateLegacyPermissions(t *testing.T) {
	u, _ := newAppServerTestUI()
	source, checkout, external := t.TempDir(), t.TempDir(), t.TempDir()
	u.session.cwd = source
	u.model, u.reasoningEffort = "model", "high"
	batch := orchestrate.Batch{Checkout: checkout, Cwd: checkout}
	for _, raw := range []string{
		`{"type":"dangerFullAccess"}`,
		`{"type":"readOnly","networkAccess":true}`,
		`{"type":"externalSandbox","networkAccess":"restricted"}`,
		fmt.Sprintf(`{"type":"workspaceWrite","networkAccess":true,"writableRoots":[%q,%q],"excludeTmpdirEnvVar":true,"excludeSlashTmp":true}`, source, external),
	} {
		// Exercise the authoritative settings decoder, including null provenance.
		wire := fmt.Sprintf(`{"method":"thread/settings/updated","params":{"threadId":"main","threadSettings":{"model":"model","effort":"high","modelProvider":"provider","approvalPolicy":"on-request","approvalsReviewer":"auto_review","activePermissionProfile":null,"sandboxPolicy":%s}}}`, raw)
		appServerTestMessage(t, u, wire)
		before, _ := json.Marshal(u.statusConfig)
		params, policy, err := u.orchestrateThreadRequest(batch, orchestrateSpawnInput{})
		if err != nil || params["sandbox"] != "read-only" || params["permissions"] != nil {
			t.Fatalf("legacy launch: %v, %s, %v", params, policy, err)
		}
		var got, want map[string]any
		if err := json.Unmarshal(policy, &got); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(raw), &want); err != nil {
			t.Fatal(err)
		}
		if roots, ok := want["writableRoots"].([]any); ok {
			roots[0] = checkout
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("policy changed: %+v; want %+v", got, want)
		}
		after, _ := json.Marshal(u.statusConfig)
		if string(before) != string(after) {
			t.Fatal("child policy changed Main")
		}
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

func TestAppServerOrchestrateConfiguredRole(t *testing.T) {
	for _, kind := range []orchestrate.WorkflowKind{orchestrate.WorkflowImplementation, orchestrate.WorkflowDiagnosis} {
		t.Run(string(kind), func(t *testing.T) {
			u, w := workflowUI(t)
			u.panes = new(nativePanePersistence)
			store := u.proxy.orchestration.store
			batch, err := store.Prepare(t.Context(), u.session.cwd, "main", "selected")
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			roleFile := filepath.Join(dir, "custom.toml")
			writeTestFile(t, roleFile, "description = 'Custom role'\ndeveloper_instructions = 'Use the configured role'\nmodel = 'role-model'\nmodel_reasoning_effort = 'medium'\nservice_tier = 'priority'\n")
			input := orchestrateSpawnInput{TaskName: "selected", Message: "Complete the assignment", Kind: kind, AgentType: "custom-role"}
			model, effort, tier := "role-model", "medium", any("priority")
			if kind == orchestrate.WorkflowDiagnosis {
				input.Model, input.ReasoningEffort, input.ServiceTier = "explicit-model", "low", new("")
				model, effort, tier = "explicit-model", "low", nil
			}
			command := &orchestrateCommand{ctx: t.Context(), workspace: u.session.cwd, main: "main", tool: "spawn_agent", input: input, reply: make(chan orchestrateResult, 1)}
			u.startOrchestratedChild(command)
			drainOrchestrateWork(t, u)
			read, params := workflowTake(t, u, w, "config/read", "", "")
			if params["cwd"] != u.session.cwd || params["includeLayers"] != true {
				t.Fatal("role lookup lost source layers", params)
			}
			config := map[string]any{"layers": []any{map[string]any{"name": map[string]any{"type": "project", "dotCodexFolder": dir}, "config": map[string]any{"agents": map[string]any{"custom-role": map[string]any{"config_file": "custom.toml"}}}}}}
			orchestrateTestReply(t, u, read, string(mustMarshalJSON(config)))
			start, params := workflowTake(t, u, w, "thread/start", "", "")
			if params["developerInstructions"] != "Use the configured role" || params["model"] != model || params["config"].(map[string]any)["model_reasoning_effort"] != effort || params["serviceTier"] != tier || params["cwd"] != batch.Cwd {
				t.Fatal("resolved role did not reach thread/start", params)
			}
			if kind == orchestrate.WorkflowDiagnosis {
				if params["sandbox"] != "read-only" || params["permissions"] != nil || params["approvalPolicy"] != "never" {
					t.Fatal("role widened diagnosis", params)
				}
			} else if params["permissions"] != ":workspace" || params["approvalsReviewer"] != "user" {
				t.Fatal("role changed host permissions", params)
			}
			response := map[string]any{"thread": map[string]any{"id": "selected-child", "cwd": batch.Cwd}, "model": model, "reasoningEffort": effort, "serviceTier": tier, "activePermissionProfile": map[string]any{"id": ":read-only"}, "sandbox": map[string]any{"type": "readOnly"}, "approvalPolicy": "never"}
			orchestrateTestReply(t, u, start, string(mustMarshalJSON(response)))
			turn, params := workflowTake(t, u, w, "turn/start", "selected-child", "")
			if kind == orchestrate.WorkflowDiagnosis && params["sandboxPolicy"].(map[string]any)["type"] != "readOnly" {
				t.Fatal("diagnosis turn became writable", params)
			}
			orchestrateTestReply(t, u, turn, `{"turn":{"id":"selected-turn"}}`)
			result := <-command.reply
			if result.err != nil || result.batch.Launch.ThreadID != "selected-child" {
				t.Fatal("launch failed", result.err)
			}
			if err := os.Remove(roleFile); err != nil {
				t.Fatal(err)
			}
			// Reopen durable state. Repeats use frozen input/settings, not files or Main's new model.
			u.proxy.orchestration.store = &orchestrate.Store{Directory: store.Directory}
			u.model = "changed-main"
			u.startOrchestratedChild(command)
			drainOrchestrateWork(t, u)
			if repeated := <-command.reply; repeated.err != nil || w.Len() != 0 {
				t.Fatal("repeat reread configuration", repeated.err, w.String())
			}
			command.input.AgentType = "different-role"
			u.startOrchestratedChild(command)
			drainOrchestrateWork(t, u)
			if changed := <-command.reply; changed.err == nil || w.Len() != 0 {
				t.Fatal("changed role reused launch", changed.err, w.String())
			}
			// Ordinary fresh-host resume has no live coordinator. Retained role instructions
			// survive deleted files; newer host execution settings still win.
			view := u.navigation.views["selected-child"]
			appServerTestNotify(t, view, "thread/settings/updated", map[string]any{"threadId": "selected-child", "threadSettings": map[string]any{"model": "later-model", "effort": "high", "serviceTier": "flex"}})
			reopened, err := openMekugiReplayStore(u.proxy.replayStore.directory)
			if err != nil {
				t.Fatal(err)
			}
			next, wire := newAppServerTestUI()
			next.ctx, next.resumeThread, next.panes = t.Context(), "selected-child", new(nativePanePersistence)
			next.proxy = &mekugiProxy{journals: newJournalStore(), replayStore: reopened}
			if err := next.requestResume("selected-child"); err != nil {
				t.Fatal(err)
			}
			read = btwTestRequest(t, wire, "thread/read", "selected-child")
			btwTestReply(t, next, read, string(mustMarshalJSON(map[string]any{"thread": response["thread"]})))
			_, params = workflowTake(t, next, wire, "thread/resume", "selected-child", "")
			restored := params["config"].(map[string]any)
			if params["permissions"] != ":read-only" || params["developerInstructions"] != "Use the configured role" || restored["model"] != "later-model" || restored["model_reasoning_effort"] != "high" || params["serviceTier"] != "flex" {
				t.Fatal("resume lost frozen role or newer host settings", params)
			}
		})
	}
}

func TestAppServerOrchestrateRejectedRole(t *testing.T) {
	u, w := workflowUI(t)
	store := u.proxy.orchestration.store
	if _, err := store.Prepare(t.Context(), u.session.cwd, "main", "rejected"); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "agents", "invalid.toml"), "name = 'invalid-native'\ndescription = 'Invalid nickname metadata'\ndeveloper_instructions = 'Diagnose without edits'\nnickname_candidates = 42\n")
	for _, role := range []string{"missing-role", "invalid-role", "invalid-native"} {
		command := &orchestrateCommand{ctx: t.Context(), workspace: u.session.cwd, main: "main", tool: "spawn_agent", input: orchestrateSpawnInput{TaskName: "rejected", Message: "Diagnose", Kind: orchestrate.WorkflowDiagnosis, AgentType: role}, reply: make(chan orchestrateResult, 1)}
		u.startOrchestratedChild(command)
		drainOrchestrateWork(t, u)
		read, _ := workflowTake(t, u, w, "config/read", "", "")
		config := map[string]any{"layers": []any{map[string]any{"name": map[string]any{"type": "project", "dotCodexFolder": dir}, "config": map[string]any{"agents": map[string]any{"invalid-role": 42}}}}}
		orchestrateTestReply(t, u, read, string(mustMarshalJSON(config)))
		select {
		case result := <-command.reply:
			if result.err == nil || !strings.Contains(result.err.Error(), role) || w.Len() != 0 {
				t.Fatal("invalid role reached child creation", result.err, w.String())
			}
		default:
			t.Fatal("invalid role reached child creation", w.String())
		}
	}
	batches, err := store.Snapshot(u.session.cwd, "main")
	if err != nil || batches[1].Launch != nil || batches[1].Workflow != nil {
		t.Fatal("rejected role retained a launch", batches, err)
	}
}

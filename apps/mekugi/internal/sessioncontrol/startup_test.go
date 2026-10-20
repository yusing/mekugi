package sessioncontrol

import (
	"errors"
	"maps"
	"reflect"
	"testing"

	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"
)

func TestStartupResumeUsesPreflightAndCapturedEvidence(t *testing.T) {
	transport := &requestTransport{id: "environment"}
	requests := NewRequests[string](transport)
	if err := requests.Initialized(true); err != nil {
		t.Fatal(err)
	}
	if len(transport.sends) != 2 || transport.sends[0].method != "initialized" || transport.sends[0].request || transport.sends[1].method != "environment/info" {
		t.Fatalf("initialization ordering: %+v", transport.sends)
	}
	startup := Startup{Thread: "--last", Workspace: "/workspace", Config: map[string]any{"model_provider": "routed", "service_tier": "priority"}}
	control := Live{}
	transport.id = "latest"
	if opening, err := requests.OpenSession(&startup, &control, true, "preflight"); err != nil || opening != OpeningLatest {
		t.Fatalf("latest: %v %v", opening, err)
	}
	if params := transport.sends[2].params.(map[string]any); params["cwd"] != "/workspace" || params["limit"] != 1 {
		t.Fatalf("latest scope: %+v", params)
	}
	if err := startup.Latest([]byte(`{"data":[]}`)); err == nil || startup.Thread != "--last" {
		t.Fatal("empty latest lookup silently started a fresh session")
	}
	if err := startup.Latest([]byte(`{"data":[{"id":"saved"}]}`)); err != nil {
		t.Fatal(err)
	}
	transport.id = "read"
	if opening, err := requests.OpenSession(&startup, &control, true, "preflight"); err != nil || opening != OpeningResume {
		t.Fatalf("preflight: %v %v", opening, err)
	}
	response, found := requests.Take("read")
	if !found || response.Owner != "preflight" || response.Method != "thread/read" || response.Scope != "main" {
		t.Fatalf("preflight authority: %+v", response)
	}
	evidence := preferences.Evidence{Model: "saved-model", TierKnown: true}
	original := maps.Clone(startup.Config)
	transport.id = "resume"
	if err := requests.Resume(&startup, &control, evidence, true); err != nil {
		t.Fatal(err)
	}
	params := transport.sends[4].params.(map[string]any)
	if params["threadId"] != "saved" || params["serviceTier"] != "priority" || params["modelProvider"] != "routed" || !control.ResumeEffort || startup.Evidence != evidence || !reflect.DeepEqual(startup.Config, original) {
		t.Fatalf("resume lost evidence/overrides: %+v %+v", params, startup)
	}
	if _, changed := params["config"].(map[string]any)["model_reasoning_effort"]; changed {
		t.Fatal("nullable effort was encoded as a TOML default")
	}
	control.Thread = "old"
	control.Replacement.Resume("saved")
	if _, err := startup.Accept(&control, "thread/resume", []byte(`{"thread":{"id":"foreign"}}`)); err == nil || control.Thread != "old" || !control.Replacement.Pending() {
		t.Fatal("foreign acceptance retired the old session")
	}
	accepted, err := startup.Accept(&control, "thread/resume", []byte(`{"reasoningEffort":"high","thread":{"id":"saved"},"collaborationMode":{"mode":"default","settings":{"reasoning_effort":"high","developer_instructions":"retained"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	control.LeaveThread()
	control.Settings.RestoreEffort = control.TakeResumeEffort()
	repair, err := accepted.RepairDefaultEffort(&control)
	if err != nil || repair == nil || !control.Settings.RestoreEffort {
		t.Fatal("acceptance lost target-bound repair intent")
	}
	settings := accepted.CollaborationMode["settings"].(map[string]any)
	if settings["reasoning_effort"] != nil || settings["developer_instructions"] != "retained" || accepted.CollaborationMode["mode"] != "default" {
		t.Fatalf("repair changed unrelated mode settings: %+v", repair)
	}
}

func TestStartupPickerIsNotAnInSessionResumeFlag(t *testing.T) {
	transport := &requestTransport{id: "read"}
	requests := NewRequests[struct{}](transport)
	startup := Startup{Thread: ResumePicker}
	control := Live{Thread: "main"}
	if opening, err := requests.OpenSession(&startup, &control, false, struct{}{}); err != nil || opening != OpeningPicker || len(transport.sends) != 0 {
		t.Fatal("startup picker sent a host request")
	}
	control.Replacement.Resume("--last")
	if opening, err := requests.OpenSession(&startup, &control, false, struct{}{}); err != nil || opening != OpeningResume || transport.sends[0].method != "thread/read" {
		t.Fatal("literal replacement target became a startup flag")
	}
}

func TestStartupForkUsesCurrentSettings(t *testing.T) {
	for _, effort := range []string{"", "high"} {
		transport := &requestTransport{id: "fork"}
		requests := NewRequests[string](transport)
		startup := Startup{Config: map[string]any{"model": "launch-model"}}
		control := Live{Thread: "main"}
		applied := preferences.Applied{Model: "current-model", Effort: effort, Tier: "priority"}
		if err := requests.Fork(&startup, &control, applied, "routed", false); err != nil {
			t.Fatal(err)
		}
		params := transport.sends[0].params.(map[string]any)
		if params["threadId"] != "main" || params["model"] != applied.Model || params["modelProvider"] != "routed" || params["serviceTier"] != applied.Tier || params["sandbox"] != "danger-full-access" || !control.Replacement.Forking() || control.ResumeEffort != (effort == "") {
			t.Fatalf("fork settings: %+v %+v", params, control)
		}
		if effort != "" && params["config"].(map[string]any)["model_reasoning_effort"] != effort {
			t.Fatal("fork lost current effort")
		}
		if startup.Config["model"] != "launch-model" {
			t.Fatal("fork mutated invocation defaults")
		}
	}
	transport := &requestTransport{err: errors.New("closed")}
	requests := NewRequests[string](transport)
	startup, control := Startup{}, Live{Thread: "main"}
	if err := requests.Fork(&startup, &control, preferences.Applied{}, "", true); err == nil || control.Replacement.Pending() {
		t.Fatal("transport failure left a replacement gate")
	}
}

package router

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/uisnapshot"
)

func requireOrchestrateToolMechanics(t *testing.T, input string) {
	t.Helper()
	for _, policy := range []string{"periodic progress", "Plan independent", "Dispatch all batches", "prepare: task_name", "spawn_agent: task_name", "queues sibling input", "Repeats return retained"} {
		if strings.Contains(input, policy) {
			t.Fatalf("tool instructions include workflow policy %q: %s", policy, input)
		}
	}
	var required []string
	if strings.Contains(input, "Your coordinator is main.") {
		required = []string{"Implement and validate your assigned changes", "local commits are part of delivery", "Main reviews and integrates", "read-only assignments"}
	} else {
		required = []string{"Children implement and validate", "Main coordinates, reviews and integrates", "local commits and linear Git integration", "explicit user restrictions", "Diagnosis-only assignments remain read-only"}
	}
	for _, rule := range required {
		if !strings.Contains(input, rule) {
			t.Fatalf("workflow omitted responsibility %q: %s", rule, input)
		}
	}
}

func TestAppServerOrchestrateWorkflowComposer(t *testing.T) {
	for _, busy := range []bool{false, true} {
		t.Run(map[bool]string{false: "start", true: "steer"}[busy], func(t *testing.T) {
			u, wire := newAppServerTestUI()
			u.proxy = &mekugiProxy{orchestration: &orchestrateRuntime{}}
			u.session.cwd = t.TempDir()
			writeTestFile(t, filepath.Join(u.session.cwd, "issue.md"), "attached issue")
			method := "turn/start"
			if busy {
				u.turn, method = "running", "turn/steer"
			}
			appServerTestKeys(t, u, "/orchestrate Issue A\nIssue B ")
			bindComposerFile(u, "@issue.md", "issue.md")
			appServerTestKeys(t, u, "\r")
			request := btwTestRequest(t, wire, method, "main")
			var input string
			for _, part := range request.Params.Input {
				input += part.Text
			}
			if strings.Count(input, orchestrateWorkflow) != 1 || !strings.Contains(input, "Issue A\nIssue B") || strings.Contains(input, "/orchestrate Issue") {
				t.Fatalf("workflow input = %q", input)
			}
			requireOrchestrateToolMechanics(t, input)
			if len(u.submission.files) != 1 || u.submission.text[u.submission.files[0].start:u.submission.files[0].end] != "@issue.md" || !strings.Contains(input, "attached issue") {
				t.Fatal("workflow lost its attached file or token position")
			}
		})
	}
}

func TestAppServerOrchestrateWorkflowUnavailable(t *testing.T) {
	for _, text := range []string{"/orchestrate", "/orchestrate issue"} {
		u, wire := newAppServerTestUI()
		appServerTestKeys(t, u, text+"\r")
		if wire.Len() != 0 || strings.TrimSpace(u.draft) != text || u.notice == "" {
			t.Fatalf("unavailable command lost its draft or dispatched: %q %q %s", u.draft, u.notice, wire.String())
		}
	}
}

func TestUISnapshotOrchestrateCommand(t *testing.T) {
	u, _ := newAppServerTestUI()
	appServerTestKeys(t, u, "/orc")
	uisnapshot.Assert(t, "testdata/snapshots/orchestrate-command-picker.txt", strings.Join(u.renderPicker(70, 4), "\n")+"\n")
}

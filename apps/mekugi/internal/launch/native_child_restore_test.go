package launch

import (
	json "encoding/json/v2"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/inference"
	"github.com/yusing/mekugi/contracts/observation"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
)

// Both publications come from their runtime's real Activity sources. The parent
// owns only the logical child identity, never the child's restored host history.
func projectRestoredChild(t *testing.T, child *appServerUI) *activity.Publication {
	t.Helper()
	parent := newAppServerSessionTestUI(t, t.TempDir())
	parent.attached.session.RegisterThread(observation.Thread{ID: child.Thread, Path: "/root/worker", Role: "worker"})
	sessionpresentation.ApplyActivity(parent.attached.source, parent.attached.childSource, nil, parent.attached.session.Roster())
	frame := sessionapi.NativeFrame{
		View:   sessionpresentation.View{Main: sessionpresentation.Thread{ID: parent.Thread}, Displayed: sessionpresentation.Thread{ID: parent.Thread}},
		Agents: parent.attached.childSource.Publication(parent.attached.presentation, parent.attached.source),
	}
	n := nativeViews{threads: &threadExecution{}}
	n.publishChild(&nativeSessionView{}, &frame, map[string]sessionapi.Descendant{child.Thread: {
		Main:   child.attached.source.Publication(child.attached.presentation, nil),
		Agents: child.attached.childSource.Publication(child.attached.presentation, child.attached.source),
	}})
	return frame.Agents
}

func restoredChildRow(t *testing.T, publication *activity.Publication) activity.Agent {
	t.Helper()
	for _, row := range publication.Roster {
		if row.Agent.Thread == "child" {
			if row.Agent.Name != "/root/worker" || row.Agent.Role != "worker" {
				t.Fatalf("parent identity lost: %+v", row.Agent)
			}
			return row.Agent
		}
	}
	t.Fatal("restored runtime missing from parent roster")
	return activity.Agent{}
}

func TestNativeIndependentChildRestoredActivity(t *testing.T) {
	for _, kind := range []string{"summary", "image", "skills"} {
		t.Run(kind, func(t *testing.T) {
			workspace := t.TempDir()
			child := newAppServerSessionTestUI(t, workspace)
			item := binding.ThreadItem{ID: "item", Type: "imageView", Path: filepath.Join(workspace, "images", "a.png")}
			want := "View `images/a.png`"
			switch kind {
			case "summary":
				if err := json.Unmarshal([]byte(`{"id":"item","type":"reasoning","summary":["Public restored summary."],"content":["PRIVATE"]}`), &item); err != nil {
					t.Fatal(err)
				}
				want = "Public restored summary."
			case "skills":
				item = binding.ThreadItem{ID: "item", Type: "commandExecution", Command: "skills-mgr get commit", Status: "completed", ExitCode: new(0)}
				want = "commit"
			}
			response, err := json.Marshal(map[string]any{"id": 1, "result": map[string]any{"thread": binding.ThreadInfo{ID: "child", Cwd: workspace, Turns: []binding.HistoryTurn{{ID: "saved", Status: "completed", Items: []binding.ThreadItem{item}}}}}})
			if err != nil {
				t.Fatal(err)
			}
			child.Startup.Thread = "child"
			appServerTestRequest(t, child, "1", "thread/resume")
			appServerTestMessage(t, child, string(response))
			publication := projectRestoredChild(t, child)
			agent := restoredChildRow(t, publication)
			if len(agent.Summary) != 1 {
				t.Fatalf("restored summary membership: %+v", agent.Summary)
			}
			entry := agent.Summary[0]
			original := child.attached.source.Publication(child.attached.presentation, nil).Entries[0]
			if entry.Agent != "/root/worker" || !entry.Restored || entry.Seq != original.Seq || entry.TextRef != original.TextRef || entry.InputGroup != "child\x00saved" || len(entry.Values) != len(original.Values) {
				t.Fatalf("source identity or caller lost: %+v; original=%+v", entry, original)
			}
			for i, value := range entry.Values {
				if value.Source != original.Values[i].Source || value.Operation != original.Values[i].Operation || value.Section != original.Values[i].Section {
					t.Fatalf("source operation identity changed: %+v; original=%+v", value, original.Values[i])
				}
			}
			if !strings.Contains(entry.Text, want) || strings.Contains(entry.Text, "PRIVATE") {
				t.Fatalf("restored public content: %q", entry.Text)
			}
			if kind == "image" && !slices.Equal(entry.Images, []string{item.Path}) {
				t.Fatalf("viewed image binding lost: %v", entry.Images)
			}
			if kind == "skills" && !slices.Equal(agent.Skills, activity.SkillSet{"commit"}) {
				t.Fatalf("restored loaded skills lost: %v", agent.Skills)
			}
			if kind == "skills" {
				_, view, _ := nativeDetailTestViews(t, child)
				if refusal := view.openDetails(sessionapi.NativeDetailRequest{Kind: sessionapi.NativeSkillDetails, Caller: "Main"}); refusal != "" {
					t.Fatal(refusal)
				}
				if view.shell.Dialog == nil || len(view.shell.Dialog.Pages) != 1 || view.shell.Dialog.Pages[0].Body != "- commit" {
					t.Fatal("runtime skills did not open their authored details")
				}
			}
		})
	}
}

func TestNativeIndependentChildRestoredUsage(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := usageStoreFixture(t, t.TempDir())
		usage := storedUsageFixture(t, store)
		measured := usage.Observation("child", "child", "gpt-6-sol", "")
		measured.Begin()
		time.Sleep(2 * time.Second)
		measured.Observe(inference.Usage{TotalsKnown: true, InputTokens: 100_000, UncachedInputTokens: 100_000, OutputTokens: 100})
		usage.Close()
		child := newAppServerSessionTestUI(t, t.TempDir())
		setTestAppServerIntegration(child, &observationFixture{usage: storedUsageFixture(t, store)})
		child.Startup.Thread = "child"
		appServerTestRequest(t, child, "1", "thread/resume")
		appServerTestMessage(t, child, `{"id":1,"result":{"thread":{"id":"child","turns":[{"id":"saved","status":"completed","items":[]}]}}}`)
		agent := restoredChildRow(t, projectRestoredChild(t, child))
		rate, known := agent.OutputThroughput.Rate()
		if !agent.TokensKnown || agent.InputTokens != 100_000 || agent.OutputTokens != 100 || agent.Roundtrips != 1 || !agent.CostKnown || rate != 50 || !known {
			t.Fatalf("provider usage or measured throughput lost: %+v; rate=%g known=%v", agent, rate, known)
		}
	})
}

package sessionapi

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/contracts/observation"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
	"github.com/yusing/mekugi/modules/activity/terminalview"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func TestFamilyRosterKeepsObservedCompaction(t *testing.T) {
	var attached sessionpresentation.Attachment
	attached.Start("root")
	attached.RegisterThread(observation.Thread{ID: "root", Path: "/root"})
	source := sessionpresentation.ActivitySource{Model: &attached.Activity().Children}
	model := new(sessionpresentation.Model)
	model.Attach(sessionpresentation.Thread{ID: "root", Caller: "/root"}, false)
	h := NewHub(t.Context(), nil)
	frame := Frame{Session: Session{Thread: "root", Caller: "/root"}, Members: []sessionpresentation.Participant{{Thread: "root", Task: "/root", Status: sessionpresentation.WorkRunning}}}
	attached.ObserveCompactionStart("root", "compact")
	source.Apply(sessionpresentation.ActivityEvent{Agents: attached.Roster()})
	frame.Agents = source.Publication(model, nil)
	runtime := familyRuntime(h, nil, frame)
	family, _ := h.Family("root")
	got := projectRoster(family.Agents, "/root").Agents[0]
	if got.State != "waiting" || got.Wait == nil || got.Wait.Body != "Compacting context" {
		t.Fatalf("logical working status hid observed compaction: %+v", got)
	}
	attached.ObserveCompaction("root", "compact")
	source.Apply(sessionpresentation.ActivityEvent{Agents: attached.Roster()})
	frame.Agents = source.Publication(model, nil)
	runtime.server.frame.Store(new(frame))
	family, _ = h.Family("root")
	got = projectRoster(family.Agents, "/root").Agents[0]
	if got.State != "working" || got.Wait != nil {
		t.Fatalf("completed compaction hid logical working status: %+v", got)
	}
}

func TestFamilySelectedChildKeepsCanonicalIdentityAndOwnMetrics(t *testing.T) {
	h := NewHub(t.Context(), nil)
	parent := activity.Agent{AgentFacts: activity.AgentFacts{Name: "/root", Thread: "parent", ContextKnown: true, ContextTokens: 9000, InputTokens: 319800, OutputTokens: 1900, Roundtrips: 9, Cost: 1.5}}
	child := activity.Agent{AgentFacts: activity.AgentFacts{Name: "/root/worker", Thread: "child", ContextKnown: true, ContextTokens: 1000, InputTokens: 1234, OutputTokens: 56, Roundtrips: 1, Cost: .01}}
	root := familyRuntime(h, nil, Frame{Session: Session{Thread: "parent", Caller: "/root"}, Agents: &activity.Publication{Roster: []activity.AgentRow{{Agent: parent}, {Agent: activity.Agent{AgentFacts: activity.AgentFacts{Name: child.Name, Thread: child.Thread}}}}}, Members: []sessionpresentation.Participant{{Thread: "child", Parent: "parent", Task: "/root/worker"}}})
	familyRuntime(h, root, Frame{Session: Session{Thread: "child", Caller: "/root/worker"}, Agents: &activity.Publication{Roster: []activity.AgentRow{{Agent: child}, {Agent: parent}}}})
	family, _ := h.Family("child")
	if family == nil || family.Root != "parent" || len(family.Agents.Roster) != 2 {
		t.Fatalf("selected child family = %+v", family)
	}
	for _, row := range family.Agents.Roster {
		want := parent
		if row.Agent.Thread == "child" {
			want = child
		}
		got := row.Agent
		if got.Name != want.Name || got.ContextTokens != want.ContextTokens || got.InputTokens != want.InputTokens || got.OutputTokens != want.OutputTokens || got.Roundtrips != want.Roundtrips || got.Cost != want.Cost {
			t.Fatalf("%s family metrics differ from its owner: %+v, want %+v", got.Thread, got, want)
		}
	}
}

func TestFamilyOwnerKeepsSuppliedCanonicalIdentity(t *testing.T) {
	for _, test := range []struct {
		name, caller, rowName, want string
	}{
		{"root default", "", "", "/root"},
		{"root identity", "/root", "/root", "/root"},
		{"independently resumed delegate", "/root/worker", "/root/worker", "/root/worker"},
		{"retained delegate roster", "", "/root/worker", "/root/worker"},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := NewHub(t.Context(), nil)
			owner := activity.Agent{AgentFacts: activity.AgentFacts{Thread: "owner", Name: test.rowName, ContextKnown: true, ContextTokens: 1000, InputTokens: 1234, OutputTokens: 56}}
			familyRuntime(h, nil, Frame{Session: Session{Thread: "owner", Caller: test.caller}, Agents: &activity.Publication{Roster: []activity.AgentRow{{Agent: owner}}}})
			family, _ := h.Family("owner")
			if family == nil || family.Root != "owner" || len(family.Agents.Roster) != 1 {
				t.Fatalf("independent family = %+v", family)
			}
			got := family.Agents.Roster[0].Agent
			if got.Name != test.want || got.Thread != "owner" || got.ContextTokens != owner.ContextTokens || got.InputTokens != owner.InputTokens || got.OutputTokens != owner.OutputTokens {
				t.Fatalf("family ownership replaced native identity or metrics: %+v", got)
			}
		})
	}
}

func TestUISnapshotFamilyRosterResumedDelegates(t *testing.T) {
	h := NewHub(t.Context(), nil)
	frame := Frame{Session: Session{Thread: "root", Caller: "/root"}, Agents: familyActivity("root", "main")}
	frame.Members = []sessionpresentation.Participant{{Thread: "root", Task: "/root", Status: sessionpresentation.WorkRunning}}
	for i := range 30 {
		status := sessionpresentation.Completed
		if i < 3 {
			status = sessionpresentation.WorkRunning
		}
		frame.Members = append(frame.Members, sessionpresentation.Participant{
			Thread: fmt.Sprintf("child%d", i), Task: fmt.Sprintf("/root/child%d", i), Status: status,
		})
	}
	familyRuntime(h, nil, frame)
	family, _ := h.Family("root")
	v := terminalview.NewView()
	v.Update(family.Agents, terminalview.Display{Caller: "/root"})
	rows := v.RosterSheet(80, 8, time.Unix(100, 0))
	if counts := ansi.Strip(strings.Join(v.RosterSummary().Counts, " ")); counts != "◐ 3 ✓ 27" {
		t.Fatalf("resumed delegate counts = %q", counts)
	}
	if len(v.Hits) != 7 || !strings.Contains(rows[len(rows)-1], "+23 more") {
		t.Fatalf("resumed sheet did not page delegates: %q", rows)
	}
	uisnapshot.AssertTerminal(t, "testdata/snapshots/family-roster-resumed.txt", rows, 80)
	selected := frame.Members[len(frame.Members)-1].Task
	v.Update(family.Agents, terminalview.Display{Caller: selected})
	v.RosterSheet(80, 8, time.Unix(100, 0))
	found := false
	for _, hit := range v.Hits {
		found = found || hit.Agent == selected
	}
	if !found {
		t.Fatalf("selected completed delegate %s scrolled out of the sheet", selected)
	}
}

func TestFamilyColdRosterLifecycleReplacesPriorStatus(t *testing.T) {
	for _, tc := range []struct {
		state sessionpresentation.WorkStatus
		want  string
	}{
		{sessionpresentation.Pending, "responding"}, {sessionpresentation.WorkRunning, "responding"},
		{sessionpresentation.Waiting, "waiting"}, {sessionpresentation.Paused, "paused"},
		{sessionpresentation.Uncertain, "paused"}, {sessionpresentation.Completed, "answered"},
		{sessionpresentation.Failed, "error"},
	} {
		frame := Frame{Agents: &activity.Publication{Roster: []activity.AgentRow{{Agent: activity.Agent{
			Thread: "child", Status: "error", LatestKind: "error",
		}}}}, Members: []sessionpresentation.Participant{{Thread: "child", Task: "/root/child", Status: tc.state}}}
		publication := familyRoster(&frame, nil)
		got := publication.Roster[0].Agent.Status
		if got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.state, got, tc.want)
		}
		if tc.state == sessionpresentation.Paused || tc.state == sessionpresentation.Uncertain {
			projected := projectRoster(publication, "/root").Agents[0]
			if projected.State != "paused" || projected.WorkSince != 0 {
				t.Errorf("cold paused member lost its state or restarted its clock: %+v", projected)
			}
		}
	}
}

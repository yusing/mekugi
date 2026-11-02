package sessionapi

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/contracts/observation"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
	"github.com/yusing/mekugi/modules/activity/terminalview"
	threadcontrol "github.com/yusing/mekugi/modules/threadcontrol/control"
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
	frame := Frame{Session: Session{Thread: "root", Caller: "/root"}, Members: []threadcontrol.Participant{{Thread: "root", Task: "/root", Status: threadcontrol.Working}}}
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

func TestUISnapshotFamilyRosterResumedDelegates(t *testing.T) {
	h := NewHub(t.Context(), nil)
	frame := Frame{Session: Session{Thread: "root", Caller: "/root"}, Agents: familyActivity("root", "main")}
	frame.Members = []threadcontrol.Participant{{Thread: "root", Task: "/root", Status: threadcontrol.Working}}
	for i := range 30 {
		status := threadcontrol.Completed
		if i < 3 {
			status = threadcontrol.Working
		}
		frame.Members = append(frame.Members, threadcontrol.Participant{
			Thread: fmt.Sprintf("child%d", i), Task: fmt.Sprintf("/root/child%d", i), Status: status,
		})
	}
	familyRuntime(h, nil, frame)
	family, _ := h.Family("root")
	v := terminalview.NewView()
	v.Update(family.Agents, terminalview.Display{Caller: "/root"})
	rows := v.NativeRoster(80, 40, time.Unix(100, 0), false)
	if len(v.Hits) != 4 || !strings.Contains(rows[0], "27 finished") || !strings.Contains(rows[0], "4 working") || !strings.Contains(strings.Join(rows, "\n"), "+27 more") {
		t.Fatalf("finished delegates expanded on resume: %q", rows)
	}
	uisnapshot.AssertTerminal(t, "testdata/snapshots/family-roster-resumed.txt", rows, 80)
	selected := frame.Members[len(frame.Members)-1].Task
	v.Update(family.Agents, terminalview.Display{Caller: selected})
	v.NativeRoster(80, 20, time.Unix(100, 0), false)
	found := false
	for _, hit := range v.Hits {
		found = found || hit.Agent == selected
	}
	if !found {
		t.Fatalf("selected completed delegate %s disappeared", selected)
	}
	if focused := v.NativeRoster(80, 20, time.Unix(100, 0), true); len(focused) <= len(rows) {
		t.Fatal("focusing Agents did not retain the larger roster allocation")
	}
}

func TestFamilyColdRosterLifecycleReplacesPriorStatus(t *testing.T) {
	for _, tc := range []struct {
		state threadcontrol.Status
		want  string
	}{
		{threadcontrol.Pending, "responding"}, {threadcontrol.Working, "responding"},
		{threadcontrol.Waiting, "waiting"}, {threadcontrol.Paused, "paused"},
		{threadcontrol.Uncertain, "paused"}, {threadcontrol.Completed, "answered"},
		{threadcontrol.Failed, "error"},
	} {
		frame := Frame{Agents: &activity.Publication{Roster: []activity.AgentRow{{Agent: activity.Agent{
			Thread: "child", Status: "error", LatestKind: "error",
		}}}}, Members: []threadcontrol.Participant{{Thread: "child", Task: "/root/child", Status: tc.state}}}
		got := familyRoster(&frame, nil).Roster[0].Agent.Status
		if got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.state, got, tc.want)
		}
	}
}

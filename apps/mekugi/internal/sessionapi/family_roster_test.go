package sessionapi

import (
	"fmt"
	"strings"
	"testing"
	"time"

	activity "github.com/yusing/mekugi/modules/activity/presentation"
	"github.com/yusing/mekugi/modules/activity/terminalview"
	threadcontrol "github.com/yusing/mekugi/modules/threadcontrol/control"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func TestUISnapshotFamilyRosterResumedDelegates(t *testing.T) {
	h := NewHub(t.Context(), nil)
	frame := Frame{Session: Session{Thread: "root", Caller: "/root"}, Agents: familyActivity("root", "main")}
	for i := range 8 {
		frame.Members = append(frame.Members, threadcontrol.Participant{
			Thread: fmt.Sprintf("child%d", i), Task: fmt.Sprintf("/root/child%d", i), Status: threadcontrol.Completed,
		})
	}
	familyRuntime(h, nil, frame)
	family, _ := h.Family("root")
	v := terminalview.NewView()
	v.Update(family.Agents, terminalview.Display{Caller: "/root"})
	rows := v.NativeRoster(80, 20, time.Unix(100, 0), false)
	if len(v.Hits) != 3 || !strings.Contains(rows[0], "8 finished") || !strings.Contains(strings.Join(rows, "\n"), "+6 more") {
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

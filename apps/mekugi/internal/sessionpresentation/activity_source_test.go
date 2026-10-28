package sessionpresentation

import (
	json "encoding/json/v2"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/contracts/scope"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
)

func TestActivitySourceAdmissionWithoutRenderer(t *testing.T) {
	var attached scope.Store
	feeds := activity.Attached[ActivityEntry, ActivityAgent](&attached)
	main := &ActivitySource{Model: &feeds.Main, Audience: MainAudience}
	children := &ActivitySource{Model: &feeds.Children, Audience: ChildAudience}
	now := time.Unix(100, 0)
	ApplyActivity(main, children, []ActivityEntry{
		{Seq: 1, Agent: "/root", Kind: "reasoning", Text: "**Checking inputs**", Observed: now},
		{Seq: 2, Agent: "/root/worker", Kind: "reasoning", Text: "**Checking child**", Observed: now},
		{Seq: 3, Agent: "/root/worker", Kind: "final", Text: "Child result", Observed: now},
	}, []ActivityAgent{{Name: "/root"}, {Name: "/root/worker", Thread: "child"}})
	if main.Model != &feeds.Main || children.Model != &feeds.Children || len(main.Entries) != 2 || len(children.Entries) != 2 || main.Entries[1].Source.ActivitySeq != 3 {
		t.Fatalf("audience/linkage left the attached feeds: main=%+v children=%+v", main.Entries, children.Entries)
	}
	publication := children.Publication(new(Model), main)
	if len(publication.Entries) != 2 || publication.Entries[0].Text != "**Checking child**" || len(publication.Roster) != 2 {
		t.Fatalf("detached publication=%+v", publication)
	}
	children.Apply(ActivityEvent{Entries: []ActivityEntry{{Seq: 4, Agent: "/root/worker", Kind: "final", Text: "Later result", Observed: now}}})
	if len(children.Agents) != 2 || len(publication.Entries) != 2 {
		t.Fatal("entry update replaced the roster or mutated its prior publication")
	}
	children.Apply(ActivityEvent{Agents: []ActivityAgent{}})
	if len(children.Agents) != 0 {
		t.Fatal("empty roster replacement was ignored")
	}
}

func TestActivitySourceSerializationExcludesHostHandles(t *testing.T) {
	entry := ActivityEntry{Seq: 1, Agent: "Main", Kind: "text", Text: "visible", Native: &NativeItem{Thread: "private-thread"}, Message: &ActivityMessage{Text: "private-message"}}
	agent := ActivityAgent{Name: "Main", Thread: "private-thread", Turn: "private-turn"}
	raw, err := json.Marshal(ActivityEvent{Entries: []ActivityEntry{entry}, Agents: []ActivityAgent{agent}})
	if err != nil || strings.Contains(string(raw), "private-") || !strings.Contains(string(raw), "visible") {
		t.Fatalf("source serialization changed its public boundary: %s, %v", raw, err)
	}
}

func TestActivitySourceRetainsNonAttachableRoster(t *testing.T) {
	data, err := json.Marshal(ActivityEvent{Agents: []ActivityAgent{
		{Name: "/root/worker", NonAttachable: true}, {Name: "/root/codex"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	var retained ActivityEvent
	if err := json.Unmarshal(data, &retained); err != nil {
		t.Fatal(err)
	}
	source := &ActivitySource{Model: new(activity.Model[ActivityEntry, ActivityAgent])}
	source.Apply(retained)
	publication := source.Model.Publish()
	if !publication.Roster[0].Agent.NonAttachable || publication.Roster[1].Agent.NonAttachable {
		t.Fatalf("retained roster lost capability: %+v", publication.Roster)
	}
	model := new(Model)
	model.view.Main = Thread{ID: "root", Caller: "/root"}
	model.view.Displayed = model.view.Main
	if got := model.ActivityTarget("/root/worker", source); got != model.view.Main {
		t.Fatalf("replay selected nonattachable row: %+v", got)
	}
	if got := model.ActivityTarget("/root/codex", source); got.Caller != "/root/codex" {
		t.Fatalf("replay lost Codex navigation: %+v", got)
	}
}

package sessionapi

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/contracts/scope"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
	"github.com/yusing/mekugi/modules/changehistory/capture"
	changes "github.com/yusing/mekugi/modules/changehistory/presentation"
	"github.com/yusing/mekugi/modules/livepreview/preview"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	threadcontrol "github.com/yusing/mekugi/modules/threadcontrol/control"
)

func familyRuntime(h *Hub, parent *managedSession, frame Frame) *managedSession {
	if frame.FamilyRoot == "" {
		frame.FamilyRoot = frame.Session.Thread
		if parent != nil {
			frame.FamilyRoot = parent.server.frame.Load().FamilyRoot
		}
	}
	runtime := &managedSession{server: NewLive(), parent: parent}
	runtime.server.frame.Store(new(frame))
	h.running[runtime] = struct{}{}
	return runtime
}

func familyActivity(thread, text string) *activity.Publication {
	entry := activity.Entry{Facts: activity.Facts{Agent: "/root", Text: text}, Values: []activity.Value{{Kind: "text", Body: text, From: "/root", To: "Main"}}}
	return &activity.Publication{Entries: []activity.Entry{entry}, MainSummary: []activity.Entry{entry}, Roster: []activity.AgentRow{{Agent: activity.Agent{
		AgentFacts: activity.AgentFacts{Name: "/root", Thread: thread, Responding: true, ContextKnown: true, ContextTokens: 42}, Summary: []activity.Entry{entry},
	}}}}
}

func familyReview(chunks ...changes.Chunk) *changes.ReviewView {
	view := &changes.ReviewView{Workspace: "/project", Revision: 1, Coverage: "complete"}
	view.Publication = view.Publication.With(changes.GroupCaptures(chunks))
	return view
}

func TestFamilyOrdinaryPublicationsAndColdTargets(t *testing.T) {
	h := NewHub(t.Context(), nil)
	rootActivity := familyActivity("root", "main summary")
	rootActivity.Roster = append(rootActivity.Roster, activity.AgentRow{Agent: activity.Agent{AgentFacts: activity.AgentFacts{Name: "/root/history", Thread: "history", Final: true}}})
	root := familyRuntime(h, nil, Frame{Session: Session{Thread: "root", Caller: "/root", Workspace: "/project"}, Agents: rootActivity,
		Status: sessionpresentation.Label{Text: "Main working"}, Members: []threadcontrol.Participant{
			{Thread: "child", Parent: "root", Task: "/root/child", Role: "worker", Status: threadcontrol.Working},
			{Thread: "nested", Parent: "child", Task: "/root/child/nested", Role: "reviewer", Status: threadcontrol.Working},
			{Thread: "cold", Parent: "root", Task: "/root/cold", Role: "explorer", Status: threadcontrol.Completed},
		}})
	childActivity := familyActivity("child", "child summary")
	child := familyRuntime(h, root, Frame{Session: Session{Thread: "child", Caller: "/root/child", Role: "worker", Workspace: "/project"}, Agents: childActivity})
	familyRuntime(h, child, Frame{Session: Session{Thread: "nested", Caller: "/root/child/nested", Workspace: "/other"}, Agents: familyActivity("nested", "nested summary")})
	familyRuntime(h, nil, Frame{Session: Session{Thread: "unrelated"}, Agents: familyActivity("unrelated", "other family")})
	family, _ := h.Family("child") // Also covers first publication before list registration.
	if family == nil || family.Root != "root" || !slices.Equal(family.Threads, []string{"child", "cold", "nested", "root"}) {
		t.Fatalf("family targets = %+v", family)
	}
	if family.Agents.Entries != nil || family.Agents.MainSummary[0].Text != "main summary" || family.Status.Text != "Main working" {
		t.Fatalf("family navigation repeated transcript or lost root metadata: %+v", family)
	}
	rows := make(map[string]activity.AgentRow)
	for _, row := range family.Agents.Roster {
		rows[row.Agent.Thread] = row
	}
	if len(rows) != 5 || !rows["history"].Agent.Final || !rows["cold"].Agent.Final || rows["cold"].Agent.Role != "explorer" {
		t.Fatalf("retained/cold rows = %+v", rows)
	}
	if row := rows["child"]; row.Agent.Name != "/root/child" || row.Agent.Role != "worker" || !row.Agent.ContextKnown || row.Agent.ContextTokens != 42 || row.Agent.Summary[0].Agent != "/root/child" || row.Agent.Summary[0].Values[0].To != "/root/child" {
		t.Fatalf("ordinary child row = %+v", row)
	}
	if row := rows["nested"]; row.Depth != 2 || row.Agent.Role != "reviewer" {
		t.Fatalf("nested row = %+v", row)
	}
	if childActivity.Roster[0].Agent.Name != "/root" || childActivity.Roster[0].Agent.Summary[0].Values[0].To != "Main" || len(rootActivity.Roster) != 2 {
		t.Fatal("family composition mutated an ordinary publication")
	}
	for _, id := range []string{"root", "nested"} {
		if got, _ := h.Family(id); got != family {
			t.Fatalf("%s received a different family publication", id)
		}
	}
}

func TestFamilyLogicalLifecycleReachesRosterConsumers(t *testing.T) {
	h := NewHub(t.Context(), nil)
	root := familyRuntime(h, nil, Frame{Session: Session{Thread: "root"}, Agents: familyActivity("root", "main")})
	child := familyRuntime(h, root, Frame{Session: Session{Thread: "child", Caller: "/root/child", Role: "worker"}})
	view := activityui.NewView()
	for _, tc := range []struct {
		name   string
		status threadcontrol.Status
		host   activity.AgentFacts
		kind   string
		state  string
		text   string
	}{
		{name: "working while host idle", status: threadcontrol.Working, state: "working", text: "child activity"},
		{name: "pending with stale final", status: threadcontrol.Pending, host: activity.AgentFacts{Final: true}, state: "working", text: "child activity"},
		{name: "waiting across host completion", status: threadcontrol.Waiting, host: activity.AgentFacts{Final: true}, state: "waiting", text: "Waiting for"},
		{name: "continuation leaves waiting", status: threadcontrol.Working, host: activity.AgentFacts{Waiting: true}, state: "working", text: "child activity"},
		{name: "completed with stale activity", status: threadcontrol.Completed, host: activity.AgentFacts{Responding: true}, state: "done", text: "done"},
		{name: "failed while host idle", status: threadcontrol.Failed, state: "error", text: "error"},
		{name: "interrupted with stale final", status: threadcontrol.Interrupted, host: activity.AgentFacts{Final: true}, state: "idle", text: "idle"},
		{name: "working clears prior error", status: threadcontrol.Working, kind: "error", state: "working", text: "child activity"},
		{name: "paused with stale activity", status: threadcontrol.Paused, host: activity.AgentFacts{Responding: true}, state: "idle", text: "paused"},
		{name: "uncertain with stale activity", status: threadcontrol.Uncertain, host: activity.AgentFacts{Responding: true}, state: "idle", text: "paused"},
		{name: "runtime-only fallback", host: activity.AgentFacts{Waiting: true}, state: "waiting", text: "Waiting for"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rootFrame := *root.server.frame.Load()
			rootFrame.Members = []threadcontrol.Participant{{Thread: "child", Parent: "root", Task: "/root/child", Role: "worker", Status: tc.status}}
			if tc.status == "" {
				rootFrame.Members = nil
			}
			root.server.frame.Store(new(rootFrame))
			childFrame := *child.server.frame.Load()
			childFrame.Agents = familyActivity("child", "child activity")
			host := &childFrame.Agents.Roster[0].Agent
			host.Responding, host.Waiting, host.Final = tc.host.Responding, tc.host.Waiting, tc.host.Final
			host.LatestKind = tc.kind
			child.server.frame.Store(new(childFrame))
			family, _ := h.Family("child")
			rows := projectRoster(family.Agents).Agents
			if rows[0].Thread != "root" || rows[0].Name != "/root" || rows[0].State != "working" || host.Responding != tc.host.Responding || host.Waiting != tc.host.Waiting || host.Final != tc.host.Final || host.LatestKind != tc.kind {
				t.Fatal("family lifecycle changed the non-admitted root or runtime source")
			}
			index := slices.IndexFunc(rows, func(row agent) bool { return row.Thread == "child" })
			if index < 0 || rows[index].State != tc.state {
				t.Errorf("browser roster child state: want %q; rows = %+v", tc.state, rows)
			}
			if index >= 0 && (rows[index].Name != "/root/child" || rows[index].Role != "worker" || rows[index].Context == nil || *rows[index].Context != 42) {
				t.Errorf("lost runtime identity or usage: %+v", rows[index])
			}
			view.UpdateRoster(family.Agents)
			painted := ansi.Strip(strings.Join(view.NativeRoster(160, 10, time.Unix(100, 0), true), "\n"))
			for _, line := range strings.Split(painted, "\n") {
				if strings.Contains(line, "└ child  "+tc.text) {
					return
				}
			}
			t.Fatalf("terminal roster missing child state %q:\n%s", tc.text, painted)
		})
	}
}

func TestFamilyReviewIdentityCompositionAndWorkspace(t *testing.T) {
	h := NewHub(t.Context(), nil)
	shared := reviewChunk(1, "/root", "", "/project/a", "", "one\n")
	scratch := reviewChunk(2, "/root/child", "", "/tmp/scratch", "", "scratch\n")
	other := reviewChunk(1, "/root/child", "", "/other/b", "", "two\n")
	other.Workspace = "/other"
	// The evidence owner namespaces capture keys by workspace, while change
	// and call identities may repeat in independent workspaces.
	for _, chunk := range []*changes.Chunk{&shared, &scratch, &other} {
		chunk.Key = chunk.Workspace + "\x00" + chunk.Key
	}
	root := familyRuntime(h, nil, Frame{Session: Session{Thread: "root", Workspace: "/project"}, Agents: familyActivity("root", "main"), Review: familyReview(shared)})
	child := familyRuntime(h, root, Frame{Session: Session{Thread: "child", Caller: "/root/child", Workspace: "/other"}, Agents: familyActivity("child", "child"), Review: familyReview(shared, scratch, other)})
	family, diff := h.Family("child")
	if diff.Workspace != "/other" || len(diff.Files) != 2 || family.Agents.NetCounts == nil || family.Agents.NetCounts.Added != 2 || diff.Counts.Net.Added != 2 || len(h.familyProjection.groups["root"].review.Files) != 3 {
		t.Fatalf("family saved review = %+v; roster counts = %+v", diff, family.Agents.NetCounts)
	}
	if next, nextDiff := h.Family("child"); next != family || nextDiff != diff {
		t.Fatal("unchanged immutable owners replaced family or review")
	}
	_, rootDiff := h.Family("root")
	if rootDiff.Workspace != "/project" || rootDiff == diff || rootDiff.Revision != diff.Revision || len(rootDiff.Files) != 2 {
		t.Fatalf("selected-workspace diff = %+v", rootDiff)
	}
	frame := *child.server.frame.Load()
	frame.Agents = familyActivity("child", "changed activity")
	child.server.frame.Store(new(frame))
	next, nextDiff := h.Family("child")
	if next == family || nextDiff != diff {
		t.Fatal("activity progress recomposed saved review or failed to update roster")
	}
	rootFrame := *root.server.frame.Load()
	rootFrame.Status.Text = "Main idle"
	root.server.frame.Store(&rootFrame)
	status, statusDiff := h.Family("child")
	if status == next || status.Agents != next.Agents || statusDiff != diff {
		t.Fatal("root status progress cloned navigation history or saved review")
	}
	frame.Review = familyReview(other)
	frame.Review.Revision++
	child.server.frame.Store(new(frame))
	_, revised := h.Family("child")
	if revised == diff || revised.Revision <= diff.Revision || len(diff.Files) != 2 {
		t.Fatal("review replacement lost identity or mutated its predecessor")
	}
}

func TestFamilyPublicationKeepsOrdinaryTranscript(t *testing.T) {
	h := NewHub(t.Context(), nil)
	root := familyRuntime(h, nil, Frame{Session: Session{Thread: "root", Workspace: "/project"}, Agents: familyActivity("root", "root only")})
	child := familyRuntime(h, root, Frame{Session: Session{Thread: "child", Caller: "/root/child", Workspace: "/project"}, Main: familyActivity("child", "child transcript"), Agents: familyActivity("child", "child only"), Review: familyReview(reviewChunk(1, "/root/child", "", "/project/a", "", "text\n"))})
	child.server.family = func() (*Family, *changes.ReviewView) { return h.Family("child") }
	sub := reviewSubscriber(t, child.server)
	frame := *child.server.frame.Load()
	publishReviewFrame(t, child.server, frame)
	event := reviewEvent(t, sub, "snapshot")
	if !strings.Contains(string(event["roster"]), "root") || !strings.Contains(string(event["main"]), "child transcript") || strings.Contains(string(event["main"]), "root only") || string(event["review"]) == "null" {
		t.Fatalf("shared selected publication = %s", event)
	}
	if raw := child.server.frame.Load(); raw.Agents != frame.Agents || raw.Review != frame.Review {
		t.Fatal("family projection fed back into raw runtime input")
	}
	frame.Session.Workspace = "/different"
	publishReviewFrame(t, child.server, frame)
	patch := reviewEvent(t, sub, "patch")
	if !strings.Contains(string(patch["review"]), `"workspace":"/different"`) {
		t.Fatalf("workspace change reused stale review: %s", patch["review"])
	}
}

func TestFamilyUnchangedWorkSkipsHistoryComposition(t *testing.T) {
	h := NewHub(t.Context(), nil)
	var chunks []changes.Chunk
	for i := range 64 {
		chunks = append(chunks, reviewChunk(uint64(i+1), "/root", "", "/tmp/scratch", "", "scratch\n"))
	}
	root := familyRuntime(h, nil, Frame{Session: Session{Thread: "root", Workspace: "/project"}, Agents: familyActivity("root", "main"), Review: familyReview(chunks...)})
	h.Family("root")
	cached := testing.AllocsPerRun(20, func() { h.Family("root") })
	revised := testing.AllocsPerRun(20, func() {
		frame := *root.server.frame.Load()
		frame.Review = new(*frame.Review)
		frame.Review.Revision++
		root.server.frame.Store(&frame)
		h.Family("root")
	})
	if revised <= cached*4 {
		t.Fatalf("family cache did not skip history composition: cached %.0f, revised %.0f allocations", cached, revised)
	}
}

func TestFamilyConcurrentRuntimePublication(t *testing.T) {
	h := NewHub(t.Context(), nil)
	root := familyRuntime(h, nil, Frame{Session: Session{Thread: "root", Workspace: "/project"}, Agents: familyActivity("root", "main")})
	child := familyRuntime(h, root, Frame{Session: Session{Thread: "child", Caller: "/root/child", Workspace: "/project"}, Agents: familyActivity("child", "child")})
	var workers sync.WaitGroup
	for _, runtime := range []*managedSession{root, child} {
		frame := *runtime.server.frame.Load()
		runtime.server.family = func() (*Family, *changes.ReviewView) { return h.Family(frame.Session.Thread) }
		workers.Go(func() {
			for range 20 {
				frame.Agents = familyActivity(frame.Session.Thread, "latest")
				if err := runtime.server.Publish(frame); err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	workers.Wait()
	family, _ := h.Family("child")
	for _, row := range family.Agents.Roster {
		if row.Agent.Summary[0].Text != "latest" {
			t.Fatalf("concurrent publication lost final runtime summary: %+v", row)
		}
	}
}

func TestFamilyLogicalRootReplacementKeepsOldWorkIsolated(t *testing.T) {
	h := NewHub(t.Context(), nil)
	members := []threadcontrol.Participant{
		{Thread: "original", Task: "/root", Status: threadcontrol.Working},
		{Thread: "child", Parent: "original", Task: "/root/child", Status: threadcontrol.Working},
		{Thread: "peer", Parent: "original", Task: "/root/peer", Status: threadcontrol.Working},
		{Thread: "cold-peer", Parent: "original", Task: "/root/cold-peer"},
		{Thread: "cold-nested", Parent: "child", Task: "/root/child/cold"},
	}
	root := familyRuntime(h, nil, Frame{Session: Session{Thread: "original", Workspace: "/project"}, Members: members,
		Agents: familyActivity("original", "original Main"), Review: familyReview(reviewChunk(1, "/root", "", "/project/main", "", "main\n"))})
	child := familyRuntime(h, root, Frame{Session: Session{Thread: "child", Caller: "/root/child", Workspace: "/project"}, Members: members,
		Agents: familyActivity("child", "child still working"), Review: familyReview(reviewChunk(2, "/root/child", "", "/project/child", "", "child\n"))})
	peer := familyRuntime(h, root, Frame{Session: Session{Thread: "peer", Caller: "/root/peer", Workspace: "/project"}, Members: members, Agents: familyActivity("peer", "peer still working")})
	_, before := h.Family("child")
	h.sessions["original"] = &listedSession{server: root.server, runtime: root}
	root.server.family = func() (*Family, *changes.ReviewView) { return h.Family(root.server.frame.Load().Session.Thread) }
	publishReviewFrame(t, root.server, Frame{Session: Session{Thread: "replacement", Workspace: "/project"}, FamilyRoot: "replacement",
		Members: []threadcontrol.Participant{{Thread: "replacement", Task: "/root"}, {Thread: "new-cold", Parent: "replacement", Task: "/root/new-cold"}},
		Agents:  familyActivity("replacement", "replacement Main"), Review: familyReview(reviewChunk(3, "/root", "", "/project/replacement", "", "replacement\n"))})
	current, currentReview := h.Family("replacement")
	if unavailable, _ := h.Family("original"); unavailable != nil {
		t.Fatal("asynchronous list registration retargeted the unavailable Main")
	}
	if !slices.Equal(current.Threads, []string{"new-cold", "replacement"}) || len(current.Agents.Roster) != 2 || len(currentReview.Files) != 1 {
		t.Fatalf("replacement inherited its former children: %+v, review %+v", current, currentReview)
	}
	old, oldReview := h.Family("child")
	if old.Root != "original" || old.Status.Text != "Main unavailable" || old.Agents.MainSummary != nil || !slices.Equal(old.Threads, []string{"child", "cold-nested", "peer"}) {
		t.Fatalf("old work inherited replacement Main or offered unavailable targets: %+v", old)
	}
	if len(oldReview.Files) != 1 || oldReview.Files[0].Path != "/project/child" || oldReview.Revision == currentReview.Revision || oldReview.Revision <= before.Revision {
		t.Fatalf("old review inherited replacement content or reused identity: %+v", oldReview)
	}
	for _, row := range old.Agents.Roster {
		if row.Agent.Thread == "original" && (row.Agent.Status != "Unavailable" || row.Agent.Responding) {
			t.Fatalf("unavailable Main retained live state: %+v", row)
		}
		if row.Agent.Thread == "replacement" {
			t.Fatal("replacement Main entered the original family roster")
		}
	}
	if same, review := h.Family("peer"); same != old || review != oldReview {
		t.Fatal("selecting an old sibling replaced the logical-family cache")
	}
	if len(h.familyProjection.groups) != 2 {
		t.Fatal("active logical family projection was discarded")
	}
	// Resuming the actual Main admits a new lifetime root. Logical identity
	// rejoins it with existing children without adopting the replacement family.
	resumed := familyRuntime(h, nil, Frame{Session: Session{Thread: "original", Workspace: "/project"}, Members: members,
		Agents: familyActivity("original", "resumed Main"), Review: familyReview(reviewChunk(4, "/root", "", "/project/resumed", "", "resumed\n"))})
	rejoined, rejoinedReview := h.Family("child")
	if rejoined.Status.Text == "Main unavailable" || rejoined.Agents.MainSummary[0].Text != "resumed Main" ||
		!slices.Equal(rejoined.Threads, []string{"child", "cold-nested", "cold-peer", "original", "peer"}) || len(rejoinedReview.Files) != 2 || rejoinedReview.Revision <= oldReview.Revision {
		t.Fatalf("resumed Main did not rejoin existing logical family: %+v, review %+v", rejoined, rejoinedReview)
	}
	if same, review := h.Family("original"); same != rejoined || review != rejoinedReview {
		t.Fatal("resumed Main and existing child have different family owners")
	}
	if same, review := h.Family("replacement"); same != current || review != currentReview {
		t.Fatal("Main resume changed the unrelated replacement family")
	}
	delete(h.running, child)
	delete(h.running, peer)
	delete(h.running, resumed)
	h.Family("replacement")
	if len(h.familyProjection.groups) != 1 {
		t.Fatal("inactive logical family retained history")
	}
}

func TestFamilyIndependentPublicationKeepsProgress(t *testing.T) {
	h := NewHub(t.Context(), nil)
	familyRuntime(h, nil, Frame{Session: Session{Thread: "busy"}, Agents: familyActivity("busy", "busy family")})
	other := familyRuntime(h, nil, Frame{Session: Session{Thread: "other"}, Agents: familyActivity("other", "before")})
	h.Family("busy")
	other.server.family = func() (*Family, *changes.ReviewView) { return h.Family("other") }
	// Hold one family's computation lock as during an expensive capture
	// composition. An unrelated runtime must still publish through the Hub.
	busy := h.familyProjection.groups["busy"]
	busy.mu.Lock()
	defer busy.mu.Unlock()
	finished := make(chan error, 1)
	go func() {
		frame := *other.server.frame.Load()
		frame.Agents = familyActivity("other", "after")
		finished <- other.server.Publish(frame)
	}()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("unrelated publication waited for another family's computation")
	}
	publication, _ := h.Family("other")
	if publication.Agents.MainSummary[0].Text != "after" {
		t.Fatal("unrelated family publication lost progress")
	}
}

func TestFamilyReviewRevisionBelongsToCaptureOwner(t *testing.T) {
	for _, replacement := range []string{"runtime", "attachment"} {
		t.Run(replacement, func(t *testing.T) {
			publish := func(path, text string) *changes.ReviewView {
				publisher, err := capture.New(new(history.Service), "")
				if err != nil {
					t.Fatal(err)
				}
				store := new(scope.Store)
				view := familyReview(reviewChunk(1, "/root/child", "", path, "", text))
				publisher.PublishReview(store, &changes.Snapshot{Publication: view.Publication}, "/project", "complete", "")
				return new(publisher.ReviewView(store))
			}
			h := NewHub(t.Context(), nil)
			root := familyRuntime(h, nil, Frame{Session: Session{Thread: "root", Workspace: "/project"}})
			frame := Frame{Session: Session{Thread: "child", Workspace: "/project"}, FamilyRoot: "root", Review: publish("/project/old", "old\n")}
			child := familyRuntime(h, root, frame)
			_, before := h.Family("root")
			frame.Review = publish("/project/new", "new\nextra\n")
			if frame.Review.Revision != child.server.frame.Load().Review.Revision {
				t.Fatal("fresh capture owners did not reproduce the local revision collision")
			}
			if replacement == "runtime" {
				delete(h.running, child)
				familyRuntime(h, root, frame)
			} else {
				frame.Attachment++
				child.server.frame.Store(new(frame))
			}
			family, after := h.Family("root")
			if after.Files[0].Path != "/project/new" || after.Revision <= before.Revision || family.Agents.NetCounts.Added != 2 || before.Files[0].Path != "/project/old" {
				t.Fatalf("replacement retained old capture evidence: review %+v, counts %+v", after, family.Agents.NetCounts)
			}
			if same, review := h.Family("root"); same != family || review != after {
				t.Fatal("unchanged capture owner lost its cached publication")
			}
		})
	}
}

func TestFamilyLivePreviewsStayFreshAndScoped(t *testing.T) {
	h := NewHub(t.Context(), nil)
	root := familyRuntime(h, nil, Frame{Session: Session{Thread: "root", Caller: "/root"}})
	child := familyRuntime(h, root, Frame{Session: Session{Thread: "child", Caller: "/root/worker"},
		Live:      []preview.Preview{{ID: "edit", Thread: "child", Caller: "/root/worker", Workspace: "/other", Input: strings.Repeat("source\n", 4000)}},
		LiveScope: preview.Scope{Workspaces: map[string]map[string]bool{"/other": {"child": true}}}})
	familyRuntime(h, nil, Frame{Session: Session{Thread: "outside"}, Live: []preview.Preview{{ID: "outside"}}})
	root.server.family = func() (*Family, *changes.ReviewView) { return h.Family("root") }
	child.server.family = func() (*Family, *changes.ReviewView) { return h.Family("child") }
	source := *child.server.frame.Load()
	family, _ := h.Family("root")
	if len(family.Live) != 1 || family.Live[0].Caller != "/root/worker" || !family.LiveScope.Workspaces["/other"]["child"] {
		t.Fatalf("family lost scoped child preview: %+v", family)
	}
	if same, _ := h.Family("root"); same != family {
		t.Fatal("unchanged source rebuilt family preview")
	}
	// Roster-only updates retain the immutable preview list, without copying source.
	frame := *root.server.frame.Load()
	frame.Agents = familyActivity("root", "new status")
	root.server.frame.Store(&frame)
	if next, _ := h.Family("root"); &next.Live[0] != &family.Live[0] {
		t.Fatal("roster update rebuilt unchanged previews")
	}
	rootSub := reviewSubscriber(t, root.server)
	publishReviewFrame(t, root.server, frame)
	if event := reviewEvent(t, rootSub, "snapshot"); !strings.Contains(string(event["live"]), "source") || strings.Contains(string(event["live"]), "outside") {
		t.Fatalf("Main live publication = %s", event["live"])
	}
	if len(root.server.frame.Load().Live) != 0 {
		t.Fatal("family previews fed back into runtime source")
	}
	// A new child snapshot updates Main even when its transcript is unchanged.
	source.Live = slices.Clone(source.Live)
	source.Live[0].Input, source.Live[0].Complete = "finished", true
	child.server.frame.Store(&source)
	publishReviewFrame(t, root.server, frame)
	if event := reviewEvent(t, rootSub, "patch"); !strings.Contains(string(event["live"]), "finished") || !strings.Contains(string(event["live"]), `"Complete":true`) {
		t.Fatalf("stale Main preview: %s", event["live"])
	}
	// Child selection never gains its sibling's previews.
	familyRuntime(h, root, Frame{Session: Session{Thread: "sibling"}, Live: []preview.Preview{{ID: "sibling"}}})
	childSub := reviewSubscriber(t, child.server)
	publishReviewFrame(t, child.server, source)
	if event := reviewEvent(t, childSub, "snapshot"); strings.Contains(string(event["live"]), "sibling") {
		t.Fatal("selected child inherited sibling previews")
	}
	source.Live, source.LiveScope = nil, preview.Scope{}
	child.server.frame.Store(&source)
	delete(h.running, child)
	family, _ = h.Family("root")
	if len(family.Live) != 1 || family.Live[0].ID != "sibling" || family.LiveScope.Workspaces["/other"]["child"] {
		t.Fatal("retired child left preview or admitted scope")
	}
}

func TestFamilyPreviewCapacityKeepsNativeAttached(t *testing.T) {
	h := NewHub(t.Context(), nil)
	root := familyRuntime(h, nil, Frame{Session: Session{Thread: "thread", Caller: "/root", Workspace: "/workspace"}})
	for i := range 90 {
		thread := fmt.Sprintf("child-%03d", i)
		source := Frame{Session: Session{Thread: thread, Caller: "/root/" + thread}, LiveScope: preview.Scope{Workspaces: map[string]map[string]bool{"/workspace": {thread: true}}}}
		producer := preview.New(nil).Attach(t.Context(), new(scope.Store))
		producer.Enable()
		producer.SetScope(source.LiveScope)
		for j := range preview.MaxPreviews {
			producer.Update(preview.Preview{ID: fmt.Sprintf("%s-edit-%d", thread, j), Thread: thread, Caller: source.Session.Caller, Workspace: "/workspace", Status: preview.PreviewEdit, Input: strings.Repeat("x", 47<<10)}, false)
		}
		source.Live = producer.Current()
		producer.Close()
		familyRuntime(h, root, source)
	}
	family, _ := h.Family("thread")
	if len(family.Live) != preview.MaxPreviews || !strings.Contains(family.Live[len(family.Live)-1].Footer, "1424 more live edits") {
		t.Fatal("family preview budget or omitted count was lost")
	}
	// New Main edits remain reachable without evicting pinned child calls.
	rootSource := *root.server.frame.Load()
	for i := range preview.MaxPreviews {
		rootSource.Live = append(rootSource.Live, preview.Preview{ID: fmt.Sprintf("root-%d", i), Thread: "thread", Caller: "/root", Workspace: "/workspace", Input: "ROOT_OWN_EDIT"})
	}
	root.server.family = func() (*Family, *changes.ReviewView) { return h.Family("thread") }
	rootSub := reviewSubscriber(t, root.server)
	publishReviewFrame(t, root.server, rootSource)
	if event := reviewEvent(t, rootSub, "snapshot"); !strings.Contains(string(event["live"]), "ROOT_OWN_EDIT") {
		t.Fatal("surviving children hid Main's own edits")
	}
	combined, _ := h.Family("thread")
	if len(combined.Live) != 2*preview.MaxPreviews || combined.Live[0].ID != family.Live[0].ID {
		t.Fatal("Main's new calls displaced surviving child viewports")
	}
	family = combined

	native := NewLive().Native()
	source, request, failure := native.newView(t.Context(), requestTarget{"thread", "/workspace"}, "", localNativeView)
	if failure != nil {
		t.Fatal(failure)
	}
	defer source.close()
	frame := nativeTestFrame("thread", 1)
	frame.Family, frame.Live, frame.LiveScope = family, family.Live, family.LiveScope
	if err := native.Publish(request.Attachment, frame); err != nil {
		t.Fatal(err)
	}
	if native.views[request.Attachment].ctx.Err() != nil {
		t.Fatal("preview capacity disconnected Main")
	}
	// A retiring source releases its slots without changing another source's data.
	first := family.Live[0].Thread
	for runtime := range h.running {
		raw := runtime.server.frame.Load()
		if raw.Session.Thread == first {
			delete(h.running, runtime)
		} else if len(raw.Live) > 0 && raw.Live[len(raw.Live)-1].Footer != "" {
			t.Fatal("display bound modified source")
		}
	}
	next, _ := h.Family("thread")
	if len(next.Live) != 2*preview.MaxPreviews || next.Live[0].Thread == first {
		t.Fatal("retired source retained bounded slots")
	}
}

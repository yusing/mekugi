package sessionapi

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/contracts/observation"

	activityfeed "github.com/yusing/mekugi/modules/activity/feed"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
)

func TestChildReplySummaryFollowsFeature(t *testing.T) {
	t.Parallel()
	reply := activitypresentation.Entry{Values: []activitypresentation.Value{{Kind: "final", Body: "## Summary\nDone.\n## Details\nEvery step.", ReplySummary: "Done."}}}
	reply.Seq, reply.Agent, reply.Kind = 1, "/root/docs", "final"
	for _, enabled := range []bool{true, false} {
		server := New()
		if err := server.Publish(Frame{Main: &activitypresentation.Publication{Entries: []activitypresentation.Entry{reply}}, ChildReplySummary: enabled}); err != nil {
			t.Fatal(err)
		}
		data, err := server.snapshot()
		if err != nil {
			t.Fatal(err)
		}
		// The original reply is always sent; its summary only when enabled.
		if event := string(data.Data); !strings.Contains(event, `"body":"## Summary`) || strings.Contains(event, `"summary":"Done."`) != enabled {
			t.Fatalf("feature %v: snapshot = %s", enabled, event)
		}
	}
}

// A compound command's result row repeats nothing its listed steps already
// show, and names the command only when no step is listed.
func TestBatchResultRow(t *testing.T) {
	t.Parallel()
	const script = "mcat a.go 1:9; mcat b.go 3:7"
	result := activitypresentation.Value{Content: activityfeed.Content{Kind: "op", Verb: "Run", Label: "shell batch"}, BatchExit: true, Duration: 3700 * time.Millisecond, Tail: []string{"}"}, TailOmitted: 143}
	steps := activitypresentation.Value{Content: activityfeed.Content{Kind: "reads", Verb: "Read", Reads: []activityfeed.Read{{Path: "a.go", Ranges: []string{"1:9"}}, {Path: "b.go", Ranges: []string{"3:7"}}}}}
	command := &activitypresentation.ItemContent{Command: activitypresentation.Command{Display: script}}

	listed := projectEntry(activitypresentation.Entry{Values: []activitypresentation.Value{steps, result}, Command: command}, false)
	if len(listed.Values) != 2 {
		t.Fatalf("values = %+v", listed.Values)
	}
	if r := listed.Values[1]; !r.BatchExit || r.Label != "" || r.Code != "" || r.Duration != 3700 || r.TailOmitted != 143 || len(r.Tail) != 1 {
		t.Fatalf("result under steps = %+v", r)
	}
	alone := projectEntry(activitypresentation.Entry{Values: []activitypresentation.Value{result}, Command: command}, false)
	if r := alone.Values[0]; r.Label != "" || r.Code != script || r.Lang != "bash" || r.TailOmitted != 143 {
		t.Fatalf("result alone = %+v", r)
	}
}

// The programs that made an edit leave its label as a separate list.
func TestEditSources(t *testing.T) {
	t.Parallel()
	edit := activitypresentation.Value{Content: activityfeed.Content{Kind: "op", Verb: "Edit", Label: "`a.go` +2 −1 · python3, gofmt", EditSource: "python3, gofmt"}}
	if v := projectEntry(activitypresentation.Entry{Values: []activitypresentation.Value{edit}}, false).Values[0]; v.Label != "`a.go` +2 −1" || strings.Join(v.Sources, "|") != "python3|gofmt" {
		t.Fatalf("edit = %+v", v)
	}
}

func TestWaitOutcomeProjection(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ result, reason, timeout string }{
		{`{"id":"w","state":"registered"}`, "", "2s"},
		{`{"id":"w","state":"timeout"}`, "timeout", ""},
		{`{"id":"w","state":"completed","event":{"task":"/root/child","status":"completed"}}`, "completed", ""},
	} {
		session := new(sessionpresentation.Attachment)
		entry := session.CoordinationEntry(sessionpresentation.ActivityEntry{Native: &sessionpresentation.NativeItem{Phase: "item/completed"}}, observation.Item{
			Type: "mcpToolCall", Server: "threadcontrol", Tool: "wait_agent", Status: "completed",
			Arguments: []byte(`{"target":"/root/child","timeout_ms":2000}`),
			Result:    []byte(`{"content":[{"type":"text","text":` + strconv.Quote(tc.result) + `}]}`),
		})
		if entry.Native.Wait == nil {
			t.Fatal("confirmed wait did not produce structured progress")
		}
		values := projectValues([]activitypresentation.Value{*entry.Native.Wait}, false)
		if values[0].WaitReason != tc.reason || values[0].WaitTimeout != tc.timeout {
			t.Fatalf("projected wait = %+v, want reason %q and timeout %q", values[0], tc.reason, tc.timeout)
		}
	}
}

func TestWaitingRosterProjection(t *testing.T) {
	t.Parallel()
	lifecycle := activitypresentation.AgentLifecycle{}
	lifecycle.SetWaiting(true, time.Unix(100, 0))
	wait := &activitypresentation.Value{Kind: "progress", Body: "Waiting for", WaitTargets: []activitypresentation.WaitTarget{{Name: "/root/worker"}}, WaitTimeout: "1s", Live: true}
	facts := activitypresentation.AgentFacts{Name: "/root", Waiting: lifecycle.Waiting, Responding: lifecycle.Responding, Final: lifecycle.Final, Wait: wait}
	roster := projectRoster(&activitypresentation.Publication{Roster: []activitypresentation.AgentRow{{Agent: activitypresentation.Agent{AgentFacts: facts, LatestKind: "error"}}}})
	got := roster.Agents[0]
	if got.State != "waiting" || got.Wait == nil || got.Wait.Body != "Waiting for" || len(got.Wait.Waits) != 1 || got.Wait.Waits[0].Name != "/root/worker" || got.Wait.WaitTimeout != "1s" {
		t.Fatalf("waiting lifecycle lost its publication: %+v", got)
	}
	wait.WaitTargets[0].Name = "changed"
	if got.Wait.Waits[0].Name != "/root/worker" {
		t.Fatal("published wait borrowed mutable owner data")
	}
	facts.Waiting = false
	settled := projectRoster(&activitypresentation.Publication{Roster: []activitypresentation.AgentRow{{Agent: activitypresentation.Agent{AgentFacts: facts, LatestKind: "error"}}}})
	if settled.Agents[0].State != "error" || settled.Agents[0].Wait != nil {
		t.Fatal("settled agent retained its prior wait")
	}
}

func TestRosterActivityUsesSharedSummary(t *testing.T) {
	for _, kind := range []string{"progress", "reasoning"} {
		item := activitypresentation.ItemContent{Facts: activitypresentation.Facts{Kind: kind, Text: "Checking callers", Observed: time.Unix(100, 0)}}
		summary := activitypresentation.Entry{Facts: item.Facts, Values: item.Values()}
		roster := projectRoster(&activitypresentation.Publication{Roster: []activitypresentation.AgentRow{{Agent: activitypresentation.Agent{
			AgentFacts: activitypresentation.AgentFacts{Name: "/root/worker", Responding: true}, Summary: []activitypresentation.Entry{summary},
		}}}})
		got := roster.Agents[0].Activity
		if got == nil || got.Kind != kind || got.Observed != 100_000 || len(got.Values) != 1 {
			t.Fatalf("%s summary unavailable without a feed entry: %+v", kind, got)
		}
		if got.Text != "" || got.Values[0].Body != "Checking callers" {
			t.Fatalf("summary content lost or duplicated: %+v", got)
		}
	}
}

func TestPendingCommandsAndEffects(t *testing.T) {
	t.Parallel()
	const source = "printf ready; printf later"
	item := activitypresentation.ItemContent{Facts: activitypresentation.Facts{Seq: 1, Kind: "tool"}, Command: activitypresentation.Command{Script: source, Display: source, Running: true, Segments: []activitypresentation.CommandSegment{
		{Source: "printf ready", Text: activitypresentation.ShellSource("printf ready", ""), Running: true},
		{Source: "printf later", Pending: true},
	}}, Details: &activitypresentation.ItemDetails{CommandText: source}}
	projected := projectEntry(activitypresentation.Entry{Values: item.Values()}, false)
	if len(projected.Values) < 2 || !projected.Values[1].Pending {
		t.Fatalf("pending command lost: %+v", projected.Values)
	}
	effect := activitypresentation.Value{Effect: true, Content: activityfeed.Content{Kind: "op", Verb: "Create", Label: "`a.go` +1 -0"}}
	if !projectEntry(activitypresentation.Entry{Values: []activitypresentation.Value{effect}}, false).Values[0].Effect {
		t.Fatal("captured effect lost its parent relationship")
	}
}

func TestTemporaryEditDisclosure(t *testing.T) {
	t.Parallel()
	for _, root := range []string{"/tmp/tree", "/workspace/tree"} {
		for _, verb := range []string{"Create", "Delete"} {
			counts, writer := "+3 -0", "cp"
			if verb == "Delete" {
				counts, writer = "+0 -3", "rm"
			}
			for _, count := range []int{10, 11, 200} {
				var records []string
				for i := range count {
					records = append(records, fmt.Sprintf("%s `%s/file%03d.go` %s · %s", verb, root, i, counts, writer))
				}
				item := activitypresentation.ItemContent{Facts: activitypresentation.Facts{Seq: 1, Kind: "tool", Text: strings.Join(records, "\n\n")}}
				values := projectEntry(activitypresentation.Entry{Values: item.Values()}, false).Values
				if len(values) != count || !strings.Contains(values[count-1].Label, fmt.Sprintf("file%03d.go", count-1)) {
					t.Fatal("folded capture lost its original files")
				}
				for i, value := range values {
					want := 0
					if i == 10 {
						want = count - 10
					}
					if value.HiddenEdits != want {
						t.Fatalf("%s %s %d row %d: hidden %d, want %d", verb, root, count, i, value.HiddenEdits, want)
					}
				}
			}
		}
	}
	const source = "first\nsecond\nthird\nfourth\nfifth\nsixth"
	item := activitypresentation.ItemContent{Facts: activitypresentation.Facts{Kind: "tool", Text: "Create `/tmp/large.txt`\n```text\n" + source + "\n```"}}
	if values := projectEntry(activitypresentation.Entry{Values: item.Values()}, false).Values; len(values) != 1 || !values[0].FoldSource || values[0].Code != source {
		t.Fatalf("temporary source disclosure lost original: %+v", values)
	}
}

// A live session's title changes by patch, so viewers keep their place; an
// entry is sent again only when its revision advances; and another thread
// replaces every viewer's state.
func TestPublishPatches(t *testing.T) {
	t.Parallel()
	server := NewLive()
	viewer := make(chan publication, subscriberBuffer)
	server.subscribers[viewer] = struct{}{}
	publish := func(thread, title string, e activitypresentation.Entry) publication {
		t.Helper()
		if err := server.Publish(Frame{Session: Session{Thread: thread, Title: title}, Main: &activitypresentation.Publication{Entries: []activitypresentation.Entry{e}}}); err != nil {
			t.Fatal(err)
		}
		select {
		case data := <-viewer:
			return data
		default:
			return publication{}
		}
	}
	entry := activitypresentation.Entry{Revision: 1, Values: []activitypresentation.Value{{Kind: "final", Body: "first"}}}
	entry.Seq, entry.Agent, entry.Kind = 1, "Main", "final"

	if event := publish("t", "Draft", entry); event.Kind != "snapshot" {
		t.Fatalf("first event = %+v", event)
	}
	if event := publish("t", "Fix the parser", entry); event.Kind != "patch" || !strings.Contains(string(event.Data), `"session":{"thread":"t","title":"Fix the parser"}`) || strings.Contains(string(event.Data), `"main"`) {
		t.Fatalf("title change = %+v", event)
	}
	unrevised := entry
	unrevised.Values = []activitypresentation.Value{{Kind: "final", Body: "second"}}
	if event := publish("t", "Fix the parser", unrevised); event.Kind != "" {
		t.Fatalf("unrevised entry = %+v", event)
	}
	revised := unrevised
	revised.Revision = 2
	if event := publish("t", "Fix the parser", revised); event.Kind != "patch" || !strings.Contains(string(event.Data), `"body":"second"`) {
		t.Fatalf("revised entry = %+v", event)
	}
	if event := publish("t2", "Fix the parser", revised); event.Kind != "snapshot" {
		t.Fatalf("new thread = %+v", event)
	}
}

func TestCompactReloadPublication(t *testing.T) {
	for _, skill := range []string{"", "commit"} {
		for _, completed := range []bool{false, true} {
			content := activitypresentation.ItemContent{
				Kind: "tool", Seq: 1, Text: "mcat -- /workspace/guide.md",
				Details: &activitypresentation.ItemDetails{Reload: []activitypresentation.ReloadSource{{Path: "/workspace/guide.md", Skill: skill}}},
				Command: activitypresentation.Command{Exited: completed},
			}
			if completed {
				content.Details.Reload[0].ExitCode = new(0)
			}
			if !completed {
				content.Command.Approval = "Pending Approval"
			}
			server := New()
			if err := server.Publish(Frame{Main: &activitypresentation.Publication{Entries: []activitypresentation.Entry{{Facts: content.Facts, Values: content.Values()}}}}); err != nil {
				t.Fatal(err)
			}
			data, err := server.snapshot()
			if err != nil {
				t.Fatal(err)
			}
			event := string(data.Data)
			if !strings.Contains(event, `"autoReload":true`) || strings.Contains(event, `"exitCode":0`) != completed || strings.Contains(event, `"approval":"Pending Approval"`) == completed {
				t.Fatalf("reload lifecycle lost: %s", event)
			}
			if skill == "" {
				if !strings.Contains(event, `"path":"/workspace/guide.md"`) || !strings.Contains(event, `"verb":"Attached"`) {
					t.Fatalf("reload file identity lost: %s", event)
				}
			} else if !strings.Contains(event, `"label":"commit"`) || !strings.Contains(event, `"verb":"Attached skill"`) {
				t.Fatalf("reload skill identity lost: %s", event)
			}
		}
	}
}

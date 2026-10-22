package sessionapi

import (
	"strings"
	"testing"
	"time"

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

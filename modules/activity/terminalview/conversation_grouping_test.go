package terminalview

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/modules/activity/presentation"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func TestUISnapshotConversationReasoningAndEdits(t *testing.T) {
	for _, scenario := range []struct{ titled, restored, heading bool }{{false, true, false}, {true, true, false}, {false, false, false}, {true, false, false}, {true, false, true}} {
		titled, restored := scenario.titled, scenario.restored
		t.Run(fmt.Sprintf("titled=%t/restored=%t/heading=%t", titled, restored, scenario.heading), func(t *testing.T) {
			v := NewView()
			v.Conversation = true
			v.Painter.Theme = diffrender.DarkTheme
			var entries []presentation.Entry
			for i, title := range []string{"Inspecting", "Implementing"} {
				seq := uint64(i + 1)
				body := "**" + title + "**\n\nRetained details for " + title + "."
				if !titled {
					body, title = title+" the changes.", ""
				}
				if scenario.heading {
					body = "**" + title + "**"
				}
				entries = append(entries, presentation.Entry{
					Seq: seq, Agent: "Main", Kind: "reasoning", Restored: restored, Live: !restored, ItemComplete: true,
					Values: []presentation.Value{{Kind: "summary", Source: seq, Label: title, Body: body, Thought: time.Duration(i+1) * time.Second}},
				})
			}
			for i, path := range []string{"first.go", "second.go"} {
				seq := uint64(i + 3)
				entries = append(entries, presentation.Entry{
					Seq: seq, Agent: "Main", Kind: "tool", Text: "apply_patch", Restored: true,
					Values: []presentation.Value{{Kind: "op", Source: seq, Verb: "Edit", Label: path, EditSource: "apply_patch"}},
				})
			}
			v.Update(&presentation.Publication{Entries: entries}, Display{})
			for _, expansion := range []uint8{0, 1} {
				v.Expansion = expansion
				feed := v.RenderConversation(80)
				if expansion == 0 && !slices.ContainsFunc(feed.Snippets, func(snippet Snippet) bool {
					block, ok := v.SnippetBlock(snippet)
					return ok && block.Kind == "summary"
				}) {
					t.Fatal("compacted reasoning has no rendered disclosure target")
				}
				for _, run := range v.Runs {
					if len(run.Blocks) == 0 || run.Blocks[0].Kind != "summary" {
						continue
					}
					if expansion == 0 && (len(run.Blocks) != 1 || run.Blocks[0].Thought != 3*time.Second) {
						t.Fatalf("consecutive reasoning did not compact: %+v", run.Blocks)
					}
					pages := presentation.DetailPages(Values(run.Blocks))
					if len(pages) != 2 {
						t.Fatalf("reasoning pages = %d", len(pages))
					}
					for i, page := range pages {
						if page.Value.Source != uint64(i+1) || v.Painter.DialogPage(Block{Value: page.Value}, 80).Text != entries[i].Values[0].Body {
							t.Fatalf("reasoning source/copy changed: %+v", page)
						}
					}
				}
				if text := strings.Join(feed.Lines, "\n"); !strings.Contains(text, "first.go") || !strings.Contains(text, "second.go") {
					t.Fatalf("edit paths collapsed: %s", text)
				}
				name := "conversation_grouping"
				if scenario.heading && expansion == 1 {
					name = "conversation_heading_grouping"
				}
				uisnapshot.AssertTerminal(t, fmt.Sprintf("testdata/snapshots/%s_%t_%d.txt", name, titled, expansion), feed.Lines, 80)
			}
			v.Expansion = 0
			entries = append(entries[:2], presentation.Entry{
				Seq: 5, Agent: "Main", Kind: "reasoning", Live: true,
				Values: []presentation.Value{{Kind: "summary", Source: 5, Body: "Streaming the next thought.", Live: true}},
			})
			v.Update(&presentation.Publication{Entries: entries}, Display{})
			feed := v.RenderConversation(80)
			if !strings.Contains(strings.Join(feed.Lines, "\n"), "Streaming the next thought.") {
				t.Fatal("completed grouping hid the live reasoning stream")
			}
			for _, run := range v.Runs {
				if len(run.Blocks) != 2 || !run.Blocks[1].Live || len(presentation.DetailPages(Values(run.Blocks))) != 3 {
					t.Fatalf("grouping lost live disclosure or original pages: %+v", run.Blocks)
				}
			}
		})
	}
}

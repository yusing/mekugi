package terminalview

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/modules/activity/presentation"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func TestUISnapshotConversationReasoningAndEdits(t *testing.T) {
	for _, titled := range []bool{false, true} {
		t.Run(fmt.Sprintf("titled=%t", titled), func(t *testing.T) {
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
				entries = append(entries, presentation.Entry{
					Seq: seq, Agent: "Main", Kind: "reasoning", Restored: true,
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
				uisnapshot.AssertTerminal(t, fmt.Sprintf("testdata/snapshots/conversation_grouping_%t_%d.txt", titled, expansion), feed.Lines, 80)
			}
		})
	}
}

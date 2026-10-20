package launch

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/libraries/diffrender"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func TestUISnapshotActivityConsecutiveOperations(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.Local)
	commands := []string{"command -v gopls", "command -v codex", "command -v bash", "rg needle missing", "rg needle src", "rg another src"}
	outputs := []string{"/usr/bin/gopls", "/usr/bin/codex", "/usr/bin/bash", "rg: missing: No such file or directory", "src/a.go:needle", "src/b.go:another"}
	for _, main := range []bool{true, false} {
		t.Run(fmt.Sprint(main), func(t *testing.T) {
			u := newAppServerSessionTestUI(t, t.TempDir())
			v := u.testActivity(u.shell.Transcript)
			v.Conversation, v.Bare, v.FeedOnly = main, !main, !main
			v.testAudience()
			v.Painter.Theme = diffrender.DarkTheme
			v.Clock = func() time.Time { return now }
			u.clock = v.Clock
			for i, command := range commands {
				exit := 0
				if i == 3 {
					exit = 2
				}
				item := binding.ThreadItem{ID: fmt.Sprint(i), Type: "commandExecution", Command: command, ExitCode: new(exit), AggregatedOutput: new(outputs[i])}
				appServerTestNotify(t, u, "item/started", map[string]any{"threadId": u.Thread, "turnId": "turn", "item": item})
				appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": u.Thread, "turnId": "turn", "item": item})
			}
			finishPacing(v)
			feed := v.RenderFeed(100, 80)
			rows := strings.Join(v.Viewport(feed, 80)[:len(feed.Lines)], "\n")
			plain := ansi.Strip(rows)
			if strings.Count(plain, "Inspect") != 1 || strings.Count(plain, "Search") != 1 || !strings.Contains(plain, "exit 2") {
				t.Fatalf("consecutive verbs or failure not preserved:\n%s", plain)
			}
			for _, output := range outputs {
				if !strings.Contains(plain, output) {
					t.Errorf("missing output %q", output)
				}
			}
			seen := make(map[uint64]bool)
			for _, snippet := range feed.Snippets {
				block, ok := v.SnippetBlock(snippet)
				if !ok || seen[block.Source] || block.Source == 0 {
					continue
				}
				seen[block.Source] = true
				if !terminalTest(u).openOutput(v.bind(terminalTest(u)), snippet) {
					t.Fatal("operation lost its dialog target")
				}
				page := terminalTest(u).Dialog.Pages[0]
				if page.Source != block.Source {
					t.Fatal("dialog selected a neighboring invocation")
				}
				found := false
				for i, command := range commands {
					if reflect.DeepEqual(page.Content, activitypresentation.Operations(toolActivityShell(command))[0].Content) && strings.Join(page.Tail, "\n") == outputs[i] {
						found = true
					}
				}
				if !found {
					t.Fatalf("dialog changed original source or output: %#v", page)
				}
			}
			if len(seen) != len(commands) {
				t.Fatalf("navigable invocations = %d", len(seen))
			}
			uisnapshot.Assert(t, fmt.Sprintf("testdata/snapshots/activity-consecutive-%t.txt", main), rows)
		})
	}
}

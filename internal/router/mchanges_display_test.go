package router

import (
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	mekugi "github.com/yusing/mekugi"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
)

func mchangesDisplay(t *testing.T, command, output string) *appServerUI {
	t.Helper()
	u := newAppServerSessionTestUI(t, t.TempDir())
	u.view.conversation = true
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "main", "turnId": "t", "item": map[string]any{
		"id": "cmd", "type": "commandExecution", "command": command, "status": "completed", "exitCode": 0, "aggregatedOutput": output}})
	return u
}

func mainFeed(u *appServerUI, width int) string {
	return ansi.Strip(strings.Join(u.view.renderFeed(width, 60).lines, "\n"))
}

func TestMChangesSummaryShowsEditRows(t *testing.T) {
	output := "M\t12\t3\tinternal/router/app.go\nA\t40\t0\tdoc/new.md\nRM\t1\t1\told.go => \"new => name.go\"\nD\t0\t9\tgone.go\n" +
		"?\t-\t-\tunknown.bin\t\"missing capture\"\namber3 retired (partial history)\nM +30 -2; 1 counts unavailable\n" +
		"? tool-managed: 2 missing capture; use --history for paths and full reasons\n"
	u := mchangesDisplay(t, "mchanges --summary", output)
	want := strings.Join([]string{
		"└ Ran mchanges --summary",
		"      Edited  internal/router/app.go   +12 -3 ━━━━━━━━",
		"      Created doc/new.md               +40    ━━━━━━━━",
		"      Moved   old.go → new => name.go  +1 -1  ━━━━━━━━",
		"      Deleted gone.go                  -9     ━━━━━━━━",
		"      ?       unknown.bin              missing capture",
		"              amber3                   retired (partial history)",
		"      Edited  tool-managed files       +30 -2 ━━━━━━━━ 1 counts unavailable",
		"      ?       tool-managed files       2 missing capture",
	}, "\n")
	if got := mainFeed(u, 100); !strings.Contains(got, want) {
		t.Fatalf("summary rows =\n%s\nwant\n%s", got, want)
	}
	// The rows are output: they collapse after the agent's next event.
	nextEvent(t, u, "main")
	u.view.settle(time.Now().Add(activityui.OutputDebounce))
	if got := mainFeed(u, 100); strings.Contains(got, "Edited") || !strings.Contains(got, "┆ … +8 lines") {
		t.Fatalf("summary rows did not collapse:\n%s", got)
	}
}

func TestMChangesListShowsEditRows(t *testing.T) {
	u := mchangesDisplay(t, "mchanges --list --max-tokens 400", "amber1..amber3 +12 -4\namber4 pending\namber5 +3 -1 ? managed:2\namber6 history:partial +1 -0 ?\n")
	want := strings.Join([]string{
		"└ Ran mchanges --list --max-tokens 400",
		"      amber1..amber3  +12 -4 ━━━━━━━━",
		"      amber4          pending",
		"      amber5          +3 -1  ━━━━━━━━ ? · managed:2",
		"      amber6          +1     ━━━━━━━━ history:partial · ?",
	}, "\n")
	if got := mainFeed(u, 100); !strings.Contains(got, want) {
		t.Fatalf("list rows =\n%s\nwant\n%s", got, want)
	}
}

func TestMChangesOutputRowsOnlyReadListingsAndSummaries(t *testing.T) {
	output := "M\t1\t0\ta.go\n"
	for _, command := range []string{"mchanges", "mchanges amber1", "mchanges --history", "mchanges --summary | head", "mchanges -- --summary", "echo --summary"} {
		if rows := mchangesOutputRows(appServerItem{Command: command, AggregatedOutput: &output}); rows != nil {
			t.Fatalf("%s read as change rows: %v", command, rows)
		}
	}
	// Output without any recognized row keeps its plain tail.
	plain := "error: something else\n"
	if rows := mchangesOutputRows(appServerItem{Command: "mchanges --summary", AggregatedOutput: &plain}); rows != nil {
		t.Fatalf("unrecognized output read as change rows: %v", rows)
	}
	mixed := "M\t1\t0\ta.go\nomitted 3 rows; mread r1\n"
	rows := mchangesOutputRows(appServerItem{Command: "/tmp/bin/mchanges amber1..amber4 --summary -- a.go", AggregatedOutput: &mixed})
	want := []activityui.ChangeRow{{Verb: "Edited", Label: "a.go", Added: 1}, {Note: "omitted 3 rows; mread r1"}}
	if !reflect.DeepEqual(rows, want) {
		t.Fatalf("rows = %#v, want %#v", rows, want)
	}
}

// The display reads what the store prints, so a format change there cannot
// silently fall back to plain notes.
func TestMChangesOutputRowsReadStoreOutput(t *testing.T) {
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	ctx, release, err := store.beginSession(t.Context(), "author", "")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	managed := mekugi.RenderReviewFile("", "gen.txt", "", "x\n")
	managed.Origin = "generator"
	gap := mekugi.RenderIncompleteReviewFile("gen.go", "gen.go", "capture deadline")
	gap.Origin = "go test"
	var ids []string
	for i, files := range [][]mekugi.ReviewFile{
		{mekugi.RenderReviewFile("file", "file", "old\n", "new\n"), mekugi.RenderReviewFile("", "new file", "", "a\n")},
		{mekugi.RenderReviewFile("gone", "", "a\nb\n", ""), mekugi.RenderIncompleteReviewFile("unknown", "unknown", "missing capture"), managed, gap},
		{mekugi.RenderReviewFile("before.go", "after => b.go", "a\n", "a\n")},
	} {
		call := strconv.Itoa(i)
		id, err := store.reserveChange(ctx, workspace, "author", call)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
		if err := store.put(ctx, workspace, map[string]mekugiHistory{call: {ChangeID: id, CorrelationID: call, ReviewFiles: files}}); err != nil {
			t.Fatal(err)
		}
	}
	pending, err := store.reserveChange(ctx, workspace, "author", "pending")
	if err != nil {
		t.Fatal(err)
	}
	for _, view := range []string{"summary", "list"} {
		options := changeReadOptions{workspace: workspace, ids: append(ids, pending), view: view}
		if view == "list" {
			options.ids = nil
		}
		output, err := store.readChanges(ctx, options)
		if err != nil {
			t.Fatal(err)
		}
		rows := mchangesOutputRows(appServerItem{Command: "mchanges --" + view, AggregatedOutput: &output})
		if len(rows) == 0 || len(rows) != strings.Count(output, "\n") {
			t.Fatalf("%s rows = %#v from %q", view, rows, output)
		}
		for _, row := range rows {
			if row.Label == "" {
				t.Fatalf("%s left a row unread: %#v from %q", view, row, output)
			}
		}
	}
}

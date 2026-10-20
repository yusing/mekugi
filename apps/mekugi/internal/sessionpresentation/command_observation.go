package sessionpresentation

// Source: apps/mekugi/internal/router/app_server_events.go:484:578@7166de95;
// app_server_search.go:15:52, activity_vcs.go:13:20, live_activity_blocks.go:9:14.
import (
	"cmp"
	"path/filepath"
	"strings"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/frontends"
	"github.com/yusing/mekugi/contracts/observation"
	"github.com/yusing/mekugi/libraries/pathdisplay"
	"github.com/yusing/mekugi/libraries/sourceanalysis/markdownsyntax"
	"github.com/yusing/mekugi/modules/activity/feed"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
)

// CommandWorkdir is the display form of the directory the host ran
// a command in, when it is not the workspace its activity is shown against.
// An unknown workspace or directory gets no label.
func CommandWorkdir(item observation.Item, workspace string) string {
	if item.Type != "commandExecution" || !filepath.IsAbs(item.Cwd) || !filepath.IsAbs(workspace) || filepath.Clean(item.Cwd) == filepath.Clean(workspace) {
		return ""
	}
	return pathdisplay.ForWorkspace(workspace, filepath.Clean(item.Cwd))
}

func CommandDirectory(item observation.Item, workspace string) string {
	if filepath.IsAbs(item.Cwd) {
		return item.Cwd
	}
	return workspace
}

// CommandText uses Codex's typed classification, then the shared
// display classifier for frontends that Codex does not recognize. Neither
// classification changes the executed command or retained host item. Paths
// display relative to the directory the command ran in, which
// CommandWorkdir labels when it differs from the workspace.
func CommandText(item observation.Item, cwd string) string {
	cwd = CommandDirectory(item, cwd)
	var parts []string
	for _, action := range item.CommandActions {
		switch action.Type {
		case "read":
			parts = append(parts, "Read "+markdownsyntax.QuoteCode(pathdisplay.ForWorkspace(cwd, action.Path)))
		case "listFiles":
			parts = append(parts, "List "+markdownsyntax.QuoteCode(cmp.Or(pathdisplay.ForWorkspace(cwd, action.Path), ".")))
		case "search":
			text := "Search " + markdownsyntax.QuoteCode(action.Query)
			if action.Path != "" {
				text += " in " + markdownsyntax.QuoteCode(pathdisplay.ForWorkspace(cwd, action.Path))
			}
			parts = append(parts, text)
		default:
			parts = nil
		}
		if parts == nil {
			break
		}
	}
	if len(parts) == 0 {
		return activitypresentation.ShellSource(frontends.ShellDisplaySource(DisplayCommand(item.Command)), cwd)
	}
	return strings.Join(parts, "\n\n")
}

// completeCommand adapts a host result into the shared invocation owner before
// terminal or report settlement. History has no revived start/end clock.
func (s *Attachment) CompleteCommand(native *NativeItem, item observation.Item, outputs *feed.Retention) {
	if item.Type != "commandExecution" {
		return
	}
	native.UserShell = item.UserShell
	native.Exit = item.ExitCode // Displayed outcome, separate from queued visual completion.
	if run := s.Commands()[[3]string{native.Thread, native.Turn, native.Item}]; run != nil {
		native.Invocation = run.Entry.Native.Invocation
	}
	source := CommandSource(item.Command, native.CommandCwd)
	if native.Invocation == nil {
		native.Invocation = activitypresentation.NewInvocation(source, native.CommandStarted, outputs)
	}
	native.Invocation.Complete(activitypresentation.CommandCompletion{
		Source: source, At: native.CommandEnded, Duration: ItemDuration(item), Aggregate: item.AggregatedOutput, Exit: item.ExitCode,
	})
	native.Output = native.Invocation.Output()
}

// CommandOutput keeps aggregate output even when the host reports no
// exit. Known failures carry their tail on the separate exit entry. Only confirmed
// success opens the settlement window or starts restored output collapsed.
func CommandOutput(entry *ActivityEntry, item observation.Item, settled time.Time) {
	if item.Type != "commandExecution" || !item.UserShell && item.ExitCode != nil && *item.ExitCode != 0 {
		return
	}
	entry.OutputTail, entry.OutputOmit = OutputTail(item.AggregatedOutput)
	if item.ExitCode == nil || *item.ExitCode != 0 {
		return
	}
	entry.Completed = settled
	entry.Native.Changes, entry.Native.Commit = CommandOutputRows(item)
}

// OutputTail keeps the last non-blank lines of a command's host
// output for display. It never reads beyond the host's aggregate, and counts
// the lines it leaves out.
func OutputTail(output *string) ([]string, int) {
	if output == nil {
		return nil, 0
	}
	return feed.TailOutput(*output)
}

func ToolText(item observation.Item, cwd string) string {
	if item.Type == "mcpToolCall" {
		return activitypresentation.MCPText(item)
	}
	if item.Type != "webSearch" {
		return CommandText(item, cwd)
	}
	query := item.Query
	if action := item.Action; action != nil {
		switch action.Type {
		case "openPage":
			return "Open " + markdownsyntax.QuoteCode(action.URL)
		case "findInPage":
			return "Search " + markdownsyntax.QuoteCode(action.Pattern) + " in " + markdownsyntax.QuoteCode(action.URL)
		case "search":
			query = cmp.Or(strings.Join(action.Queries, " | "), action.Query, query)
		}
	}
	return "Search " + markdownsyntax.QuoteCode(query)
}

// Count returned records only when host evidence is complete and attributable
// to one search. Missing output, errors, context rows and truncated output are
// not zero results. No command is executed to infer a count.
func SearchResults(item observation.Item) *int {
	if item.Type == "webSearch" {
		if item.Results != nil {
			return item.Results
		}
		return nil
	}
	if item.Type != "commandExecution" || item.Status != "completed" || item.ExitCode == nil || *item.ExitCode < 0 || *item.ExitCode > 1 {
		return nil
	}
	// Codex's shell aggregate can silently cap at 1 MiB.
	// Source: codex-rs/core/src/exec.rs:865:910 (DEFAULT_OUTPUT_BYTES_CAP).
	if item.AggregatedOutput != nil && len(*item.AggregatedOutput) >= 1<<20 {
		return nil
	}
	return activitypresentation.SearchResultCount(DisplayCommand(item.Command), item.AggregatedOutput, *item.ExitCode)
}

func ItemDuration(item observation.Item) time.Duration {
	if item.DurationMS == nil {
		return 0
	}
	return time.Duration(*item.DurationMS) * time.Millisecond
}

// CommandOutputRows adapts observed change-history or VCS output, retaining the
// immutable commit key whose files can complete the output-observed rows.
func CommandOutputRows(item observation.Item) ([]activitypresentation.ChangeRow, activitypresentation.CommitKey) {
	if rows := activitypresentation.MChangesOutputRows(DisplayCommand(item.Command), item.AggregatedOutput); rows != nil || item.AggregatedOutput == nil {
		return rows, activitypresentation.CommitKey{}
	}
	return activitypresentation.VCSOutputRows(frontends.ShellDisplaySource(DisplayCommand(item.Command)), item.Cwd, *item.AggregatedOutput)
}

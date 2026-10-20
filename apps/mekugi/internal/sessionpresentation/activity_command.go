package sessionpresentation

import (
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/frontends"

	"github.com/yusing/mekugi/libraries/shellinstrumentation"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
)

// Source: apps/mekugi/internal/router/activity_command.go:1:76@45fbe279

// CommandPresentation admits native facts and existing output snapshots without
// transferring handles, capture authority, or process lifetime to presentation.
func CommandPresentation(entry ActivityEntry) activitypresentation.Command {
	c := activitypresentation.Command{Source: entry.Seq, Text: entry.Text, Observed: entry.Observed, Tail: entry.OutputTail, TailOmitted: entry.OutputOmit}
	n := entry.Native
	if n == nil {
		return c
	}
	if n.Invocation != nil {
		canonical := n.Invocation.Snapshot()
		c.Script, c.Display, c.Directory = canonical.Script, canonical.Display, canonical.Directory
		c.Output, c.OutputContent = canonical.Output, len(n.Segments) > 0 && canonical.OutputContent
	} else {
		// Non-command items, including hooks, share the terminal detail surface.
		source := CommandSource(n.Command, n.CommandCwd)
		c.Script, c.Display, c.Directory = source.Script, source.Display, source.Directory
		if n.Output != nil {
			snapshot, _ := n.Output.Context(0)
			c.Output = &snapshot
			c.OutputContent = len(n.Segments) > 0 && n.Output.HasContent()
		}
	}
	// Terminal publication can lag host completion while a burst rolls through.
	// Keep this overlay local; never turn the canonical invocation back to running.
	c.Started, c.Ended, c.Duration = n.CommandStarted, n.CommandEnded, n.Duration
	c.Running, c.Approval, c.Changes = n.Running, n.Approval, n.Changes
	if n.Exit != nil {
		c.Exited, c.ExitCode = true, *n.Exit
	}
	if receipt := n.CapturedEdit; receipt != nil {
		c.Captured = true
		if len(receipt.Calls) > 0 && receipt.Calls[0] == n.Item {
			c.CaptureText = receipt.Text
		}
	}
	for _, segment := range n.Segments {
		if n.SegmentsPending {
			break
		}
		value := activitypresentation.CommandSegment{
			Source: segment.Source, Text: segment.Text, Running: segment.Running, Skipped: segment.Skipped, ExitCode: segment.Exit,
			Started: segment.Timing.Started, Ended: segment.Timing.Ended, Duration: time.Duration(segment.Timing.ElapsedNS),
			Tail: segment.Tail, TailOmitted: segment.Omit, Changes: segment.Changes,
		}
		if !segment.Running && !segment.Skipped && segment.Exit == 0 {
			value.Writer = (activitypresentation.CommandSource{Script: segment.Source, Display: frontends.ShellDisplaySource(segment.Source), Directory: ""}).WritesFiles()
		}
		if segment.Output != nil {
			snapshot, _ := segment.Output.Context(0)
			value.Output = &snapshot
			value.OutputContent = segment.Output.HasContent()
		}
		c.Segments = append(c.Segments, value)
	}
	return c
}

func CommandSource(command, directory string) activitypresentation.CommandSource {
	script, ok := shellinstrumentation.CommandScript(command)
	if !ok {
		script = shellinstrumentation.ShOriginal(command)
	}
	return activitypresentation.CommandSource{Script: script, Display: frontends.ShellDisplaySource(DisplayCommand(command)), Directory: directory}
}

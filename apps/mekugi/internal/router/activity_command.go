package router

import (
	"time"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/libraries/shellinstrumentation"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
)

// commandPresentation admits native facts and existing output snapshots without
// transferring handles, capture authority, or process lifetime to presentation.
func commandPresentation(entry activityPaneEntry) activitypresentation.Command {
	c := activitypresentation.Command{Source: entry.Seq, Text: entry.Text, Observed: entry.Observed, Tail: entry.outputTail, TailOmitted: entry.outputOmit}
	n := entry.native
	if n == nil {
		return c
	}
	c.Script = n.command
	if script, ok := binding.CommandScript(c.Script); ok {
		c.Script = script
	} else {
		c.Script = shellinstrumentation.ShOriginal(c.Script)
	}
	c.Display, c.Directory = toolActivityShellSource(appServerDisplayCommand(n.command)), n.commandCwd
	c.Started, c.Ended, c.Duration = n.commandStarted, n.commandEnded, n.duration
	c.Running, c.Approval, c.Changes = n.running, n.approval, n.changes
	if n.exit != nil {
		c.Exited, c.ExitCode = true, *n.exit
	}
	if n.output != nil {
		snapshot, _ := n.output.Context(0)
		c.Output = &snapshot
		c.OutputContent = len(n.segments) > 0 && n.output.HasContent()
	}
	if receipt := n.capturedEdit; receipt != nil {
		c.Captured = true
		if len(receipt.Calls) > 0 && receipt.Calls[0] == n.item {
			c.CaptureText = receipt.Text
		}
	}
	for _, segment := range n.segments {
		value := activitypresentation.CommandSegment{
			Source: segment.source, Text: segment.text, Running: segment.running, Skipped: segment.skipped, ExitCode: segment.exit,
			Started: segment.timing.Started, Ended: segment.timing.Ended, Duration: time.Duration(segment.timing.ElapsedNS),
			Tail: segment.tail, TailOmitted: segment.omit, Changes: segment.changes,
		}
		if c.Captured && !segment.running && !segment.skipped && segment.exit == 0 {
			value.Writer = execSegmentEdits(segment.source)
		}
		if segment.output != nil {
			snapshot, _ := segment.output.Context(0)
			value.Output = &snapshot
			value.OutputContent = c.Captured && !segment.running && !segment.skipped && segment.exit == 0 && segment.output.HasContent()
		}
		c.Segments = append(c.Segments, value)
	}
	return c
}

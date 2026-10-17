package router

import (
	"cmp"
	"slices"
	"strings"
	"time"

	"github.com/yusing/mekugi/contracts/observation"

	"github.com/yusing/mekugi/libraries/terminalui"
	"github.com/yusing/mekugi/libraries/textsanitize"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"

	activityui "github.com/yusing/mekugi/modules/activity/terminalview"
)

func appServerDuration(item observation.Item) time.Duration {
	if item.DurationMS == nil {
		return 0
	}
	return time.Duration(*item.DurationMS) * time.Millisecond
}

// Host timing and exit facts describe a single command or explicitly combined
// output, never individual rows reconstructed from a command list.
func setCommandFacts(blocks []activityui.Block, command activitypresentation.Command) {
	values := activityui.Values(blocks)
	command.ApplyFacts(values)
	for i := range blocks {
		blocks[i].Value = values[i]
	}
}

func parseLiveActivity(entry activityPaneEntry) (blocks []activityui.Block) {
	command := commandPresentation(entry)
	defer func() {
		defer func() { identifyActivityBlocks(blocks, entry.Seq) }()
		// An unsupported batch has one host clock, not a clock per classified
		// operation. Keep that total visible and its output on one explicit row.
		if n := entry.native; entry.Kind == "tool" && n != nil && n.command != "" &&
			(len(n.segments) == 0 && (n.running || n.duration > 0) && command.Compound(activityui.Values(blocks)) || n.approval != "" && len(blocks) > 1) &&
			!slices.ContainsFunc(blocks, func(b activityui.Block) bool { return b.BatchExit }) {
			batch := activityui.Block{Kind: "op", Verb: "Run", Label: "shell batch", BatchExit: true, Running: n.running, Output: retainedOutputID(n.output, 1), Tail: entry.outputTail, TailOmitted: entry.outputOmit}
			if len(n.segments) == 0 {
				for i := range blocks {
					blocks[i].Output, blocks[i].Tail, blocks[i].TailOmitted = 0, nil, 0
				}
			} else {
				batch.Output, batch.Tail, batch.TailOmitted = 0, nil, 0
			}
			blocks = append(blocks, batch)
		}
		if n := entry.native; n != nil && n.commandTimeout != "" {
			for i := range blocks {
				if !slices.Contains(blocks[i].Timeouts, n.commandTimeout) {
					blocks[i].Timeouts = append(slices.Clone(blocks[i].Timeouts), n.commandTimeout)
				}
			}
		}
		if entry.native != nil {
			setCommandFacts(blocks, command)
		}
		if entry.native != nil && len(blocks) > 0 {
			setApprovalBlocks(blocks, entry.native.approval)
		}
		if entry.Kind == "tool" && entry.native != nil && entry.native.workdir != "" && len(blocks) > 0 {
			// Every row keeps the directory for merging; one label per invocation.
			for i := range blocks {
				blocks[i].Workdir = textsanitize.Safe(entry.native.workdir, false)
			}
			blocks[0].ShowWorkdir = true
		}
	}()
	text := textsanitize.Safe(entry.Text, false)
	source := terminalui.MarkdownSource(entry.Text)
	switch entry.Kind {
	case "hook":
		n := entry.native
		if n == nil || n.hook == nil {
			return nil
		}
		return []activityui.Block{{Kind: "op", Verb: "Run", Hook: new(*n.hook), Label: text, Running: n.running,
			Output: retainedOutputID(n.output, 1), Tail: entry.outputTail, TailOmitted: entry.outputOmit}}
	case "reply":
		// Direction comes from the delivered message, never from its text.
		if entry.message != nil {
			return []activityui.Block{{Kind: "message", From: entry.message.from, To: entry.message.to, Owner: entry.Agent, Body: strings.Trim(textsanitize.Safe(entry.message.text, false), "\n")}}
		}
	case "question":
		label, body, _ := strings.Cut(text, "\n")
		block := activityui.Block{Kind: "op", Verb: "Asked", Label: label, Body: body}
		if entry.native != nil {
			block.Questions = slices.Clone(entry.native.questions)
		}
		return []activityui.Block{block}
	case "attachments":
		if entry.native != nil {
			return entry.native.attachments
		}
	case "progress":
		if entry.native != nil && entry.native.wait != nil {
			block := activityui.Block{Value: *entry.native.wait}
			block.Live = entry.native.live && entry.native.phase == "item/started"
			return []activityui.Block{block}
		}
		return []activityui.Block{{Kind: "progress", Body: text}}
	case "assignment":
		if entry.assignment != nil {
			return []activityui.Block{{Kind: "message", From: entry.assignment.from, To: entry.assignment.to, Owner: entry.Agent, Body: textsanitize.Safe(entry.assignment.text, false)}}
		}
	case "reasoning":
		blocks := activityui.ReasoningSections(source)
		for i := range blocks {
			block := &blocks[i]
			if entry.native != nil {
				block.Live = i == len(blocks)-1 && (entry.native.phase == "summary" || entry.native.phase == "item/started")
				// Duration belongs to the item, not to each detected section.
				if i == len(blocks)-1 {
					block.Thought = entry.native.thought
				}
			}
			block.Collapsed = block.Collapsible() && (entry.native == nil || !entry.native.live || entry.native.phase != "item/completed")
		}
		return blocks
	case "command":
		if entry.native != nil {
			return []activityui.Block{{Kind: "op", Verb: "Run", Label: textsanitize.Safe(entry.native.status, false), Code: textsanitize.Safe(entry.native.command, false), Lang: "bash", Fenced: true}}
		}
	case "final":
		return []activityui.Block{{Kind: "final", Body: source, Owner: entry.Agent}}
	case "start":
		if entry.start != nil {
			block := activityui.Block{Kind: "start", Label: textsanitize.Safe(entry.start.label(), false)}
			if entry.assignment != nil {
				block.From, block.To = entry.assignment.from, entry.assignment.to
				block.Body = textsanitize.Safe(entry.assignment.text, false)
			}
			return []activityui.Block{block}
		}
	case "error":
		if entry.ErrorDetail != "" {
			text = textsanitize.Safe(entry.ErrorDetail, false)
		}
		return []activityui.Block{{Kind: "error", Body: text, Label: entry.Agent}}
	case "compaction":
		return []activityui.Block{{Kind: "compaction", Body: text}}
	case "tool":
		if entry.native != nil && len(entry.native.segments) > 0 {
			return commandSegmentBlocks(command)
		}
		blocks := activityui.FoldStages(activityui.Disclosure(nil).Blocks(activitypresentation.Operations(text)))
		if len(blocks) == 1 && blocks[0].Verb == "Search" && entry.native != nil {
			if entry.native.searchResults != nil {
				blocks[0].Results = new(*entry.native.searchResults)
			}
		}
		if entry.native != nil && len(blocks) > 0 {
			blocks[len(blocks)-1].Output = retainedOutputID(entry.native.output, 1)
		}
		if entry.native != nil && (entry.native.running || len(entry.outputTail) > 0) {
			// The host provides one stream for the whole invocation. Place it
			// after the final displayed operation, not an earlier Run row.
			for i := range blocks {
				blocks[i].Running = entry.native.running
			}
			if len(blocks) > 0 {
				last := &blocks[len(blocks)-1]
				last.Tail, last.TailOmitted = entry.outputTail, entry.outputOmit
				last.Changes = entry.native.changes
				// A read's output is what the agent read; it starts collapsed.
				last.Collapsed = last.ReadOutput() && last.Collapsible()
			}
		}
		return activityui.GroupOperations(blocks)
	}
	return []activityui.Block{{Kind: "text", Body: source}}
}

// instantOperations reports tool text whose every operation prints what it
// reads at once, so its output shows whole rather than rolling through.
func instantOperations(text string) bool {
	blocks := activitypresentation.Operations(textsanitize.Safe(text, false))
	return len(blocks) > 0 && !slices.ContainsFunc(blocks, func(block activitypresentation.Value) bool { return !block.Instant() })
}

// retainBatchResult preserves the outcome and output across receipt reparses,
// replacing a synthesized timing row rather than duplicating the invocation.
func retainBatchResult(blocks []activityui.Block, batch activityui.Block) []activityui.Block {
	if at := slices.IndexFunc(blocks, func(b activityui.Block) bool { return b.BatchExit }); at >= 0 {
		blocks[at] = batch
		return blocks
	}
	return append(blocks, batch)
}

// setApprovalBlocks puts a compound invocation's state on its aggregate row.
func setApprovalBlocks(blocks []activityui.Block, outcome string) {
	if len(blocks) == 0 {
		return
	}
	at := 0
	for i := range blocks {
		blocks[i].Approval = ""
		if blocks[i].BatchExit {
			at = i
		}
	}
	blocks[at].Approval = outcome
}

// commandExitBlocks keeps a combined shell failure separate from classified
// operations: without a complete segment report none owns the batch's exit.
func commandExitBlocks(blocks []activityui.Block, code int, tail []string, omitted int) []activityui.Block {
	last, count, batch := -1, 0, -1
	var output int
	for i, block := range blocks {
		output = cmp.Or(output, block.Output)
		if block.Segment {
			return blocks // Confirmed segment outcomes already own their statuses.
		}
		if block.BatchExit {
			batch = i
		} else if block.Kind == "op" || block.Kind == "reads" {
			last, count = i, count+1
		}
	}
	if last < 0 {
		return blocks
	}
	if count > 1 || batch >= 0 {
		if batch < 0 {
			batch = len(blocks)
			blocks = append(blocks, activityui.Block{Kind: "op", Verb: "Run", Label: "shell batch", BatchExit: true})
		}
		// Any streamed tail was combined too; move it to the batch result.
		for i := range blocks {
			blocks[i].Tail, blocks[i].TailOmitted, blocks[i].Output = nil, 0, 0
		}
		last = batch
	}
	blocks[last].ExitCode, blocks[last].Exited = code, true
	blocks[last].Tail, blocks[last].TailOmitted, blocks[last].Output = tail, omitted, output
	blocks[last].Collapsed = false
	return blocks
}

// commandSegmentBlocks adds terminal disclosure to admitted segment values.
func commandSegmentBlocks(command activitypresentation.Command) []activityui.Block {
	blocks := activityui.Disclosure(nil).Blocks(command.SegmentValues(false))
	for i := range blocks {
		blocks[i].Collapsed = blocks[i].ReadOutput() && blocks[i].Collapsible()
	}
	return activityui.GroupOperations(blocks)
}

package router

import (
	"cmp"
	"slices"
	"strings"
	"time"

	"github.com/yusing/mekugi/libraries/textsanitize"

	"github.com/yusing/mekugi/libraries/shellinstrumentation"
	"github.com/yusing/mekugi/libraries/terminalui"
	activitymodel "github.com/yusing/mekugi/modules/activity/feed"

	activityui "github.com/yusing/mekugi/internal/ui/activity"
	"mvdan.cc/sh/v3/syntax"
)

func appServerDuration(item appServerItem) time.Duration {
	if item.DurationMS == nil {
		return 0
	}
	return time.Duration(*item.DurationMS) * time.Millisecond
}

// Host timing and exit facts describe a single command or explicitly combined
// output, never individual rows reconstructed from a command list.
func setCommandFacts(blocks []activityui.Block, entry activityPaneEntry) {
	if entry.native == nil {
		return
	}
	compound := commandTimingIsCompound(blocks, entry)
	for i := range blocks {
		block := &blocks[i]
		if (block.Kind != "op" && block.Kind != "reads") || block.Skipped || compound && !block.BatchExit {
			continue
		}
		if entry.native.exit != nil {
			block.ExitCode, block.Exited = *entry.native.exit, true
		}
		block.Duration, block.Started, block.Ended = entry.native.duration, entry.native.commandStarted, entry.native.commandEnded
		block.NotificationTiming = true
		if entry.native.running && block.Started.IsZero() {
			block.Started = entry.Observed
		}
	}
}

func commandTimingIsCompound(blocks []activityui.Block, entry activityPaneEntry) bool {
	compound := len(blocks) > 1 || len(entry.native.segments) > 0
	if compound || entry.native.command == "" {
		return compound
	}
	command := entry.native.command
	if script, ok := appServerShellScript(command); ok {
		command = script
	} else {
		// Offline replay retains a tracked script without its shell argv;
		// the tracking envelope is not authored compound work.
		command = shellinstrumentation.ShOriginal(command)
	}
	if program, err := syntax.NewParser().Parse(strings.NewReader(command), ""); err == nil {
		// A setup script is deliberately one full-source Run event, so its
		// invocation clock belongs to that row, not a second shell batch row.
		if len(blocks) == 1 && blocks[0].Verb == "Run" &&
			blocks[0].Code == textsanitize.Safe(toolActivityShellSource(command), false) && shellinstrumentation.HasShellSetup(program) {
			return compound
		}
		compound = compound || len(program.Stmts) > 1
		if len(program.Stmts) == 1 {
			if binary, ok := program.Stmts[0].Cmd.(*syntax.BinaryCmd); ok {
				compound = compound || binary.Op == syntax.AndStmt || binary.Op == syntax.OrStmt
			}
		}
	}
	return compound
}

func parseLiveActivity(entry activityPaneEntry) (blocks []activityui.Block) {
	defer func() {
		// An unsupported batch has one host clock, not a clock per classified
		// operation. Keep that total visible and its output on one explicit row.
		if n := entry.native; entry.Kind == "tool" && n != nil && n.command != "" &&
			(len(n.segments) == 0 && (n.running || n.duration > 0) && commandTimingIsCompound(blocks, entry) || n.approval != "" && len(blocks) > 1) &&
			!slices.ContainsFunc(blocks, func(b activityui.Block) bool { return b.BatchExit }) {
			batch := activityui.Block{Kind: "op", Verb: "Run", Label: "shell batch", BatchExit: true, Running: n.running, Output: n.output, Tail: entry.outputTail, TailOmitted: entry.outputOmit, Collapsed: n.collapsed}
			if len(n.segments) == 0 {
				for i := range blocks {
					blocks[i].Output, blocks[i].Tail, blocks[i].TailOmitted = nil, nil, 0
				}
			} else {
				batch.Output, batch.Tail, batch.TailOmitted = nil, nil, 0
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
		setCommandFacts(blocks, entry)
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
		return []activityui.Block{{Kind: "op", Verb: "Run", Hook: n.hook, Label: text, Running: n.running,
			Output: n.output, Tail: entry.outputTail, TailOmitted: entry.outputOmit, Collapsed: n.collapsed}}
	case "reply":
		// Direction comes from the delivered message, never from its text.
		if entry.message != nil {
			return []activityui.Block{{Kind: "message", From: entry.message.from, To: entry.message.to, Owner: entry.Agent, Body: strings.Trim(textsanitize.Safe(entry.message.text, false), "\n")}}
		}
	case "question":
		label, body, _ := strings.Cut(text, "\n")
		block := activityui.Block{Kind: "op", Verb: "Asked", Label: label, Body: body}
		if entry.native != nil {
			block.Questions = entry.native.questions
		}
		return []activityui.Block{block}
	case "attachments":
		if entry.native != nil {
			return entry.native.attachments
		}
	case "progress":
		if entry.native != nil && entry.native.wait != nil {
			block := *entry.native.wait
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
				if i == len(blocks)-1 && entry.native.thought >= time.Second {
					block.Elapsed = activityui.FormatDuration(entry.native.thought)
				}
			}
			block.Collapsed = block.Collapsible() && (entry.native == nil || !entry.native.live || entry.native.collapsed || entry.native.phase != "item/completed")
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
			return commandSegmentBlocks(entry, false)
		}
		blocks := activityui.FoldStages(toolOperationBlocks(text))
		if len(blocks) == 1 && blocks[0].Verb == "Search" && entry.native != nil {
			blocks[0].Results = entry.native.searchResults
		}
		if entry.native != nil && len(blocks) > 0 {
			blocks[len(blocks)-1].Output = entry.native.output
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
				last.Changes = commitChanges(entry.native.changes, entry.native.commit)
				// A read's output is what the agent read; it starts collapsed.
				last.Collapsed = (entry.native.collapsed || last.ReadOutput()) && last.Collapsible()
			}
		}
		return activityui.GroupOperations(blocks)
	}
	return []activityui.Block{{Kind: "text", Body: source}}
}

// toolOperationBlocks attaches terminal blocks to parsed invocation content,
// merging adjacent targets without inventing separate invocation pages.
func toolOperationBlocks(text string) []activityui.Block {
	content := activitymodel.ParseOperations(text)
	blocks := make([]activityui.Block, len(content))
	for i, value := range content {
		blocks[i] = activityui.Block{Content: value}
	}
	blocks = activityui.MergeLiveActivityReads(blocks)
	// These targets share one invocation's aggregate. Dialog pages are
	// formed only when separate invocations merge later in the feed.
	for i := range blocks {
		blocks[i].Members = nil
	}
	return blocks
}

// instantOperations reports tool text whose every operation prints what it
// reads at once, so its output shows whole rather than rolling through.
func instantOperations(text string) bool {
	blocks := toolOperationBlocks(textsanitize.Safe(text, false))
	return len(blocks) > 0 && !slices.ContainsFunc(blocks, func(block activityui.Block) bool { return !block.Instant() })
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
	var output *activitymodel.Output
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
			blocks[i].Tail, blocks[i].TailOmitted, blocks[i].Output = nil, 0, nil
		}
		last = batch
	}
	blocks[last].ExitCode, blocks[last].Exited = code, true
	blocks[last].Tail, blocks[last].TailOmitted, blocks[last].Output = tail, omitted, output
	blocks[last].Collapsed = false
	return blocks
}

// commandSegmentBlocks shows each segment of a tracked command as its own
// operations, with that segment's state, exit status, and output after its
// last operation. Without per-segment output, the host's combined output
// follows the last segment shown. A captured edit's output and timing follow
// an Edited row naming its paths; only dialog pages carry the edit's source.
func commandSegmentBlocks(entry activityPaneEntry, dialog bool) []activityui.Block {
	var blocks []activityui.Block
	receipt := entry.native.capturedEdit
	if receipt != nil && len(receipt.calls) > 0 && receipt.calls[0] == entry.native.item {
		// Counts belong to the capture as a whole, not any individual segment.
		blocks = append(blocks, toolOperationBlocks(textsanitize.Safe(receipt.text, false))...)
	}
	edited := func(paths []string, source string) activityui.Block {
		block := activityui.Block{Kind: "op", Verb: "Edited", Label: strings.Join(paths, ", ")}
		if dialog {
			block.Code, block.Lang, block.Fenced = textsanitize.Safe(source, false), "bash", true
		}
		return block
	}
	// Classify decoration in the full script, not as a standalone segment.
	// Keep failed headings visible, and retain every segment for replay/output.
	command := entry.native.command
	if script, ok := appServerShellScript(command); ok {
		command = script
	}
	_, classified := toolActivityReads(command, "")
	for _, segment := range entry.native.segments {
		if classified && !segment.skipped && segment.exit == 0 {
			if program, err := syntax.NewParser().Parse(strings.NewReader(segment.source), ""); err == nil && len(program.Stmts) == 1 && toolActivityReadSeparator(program.Stmts[0]) {
				continue
			}
		}
		operations := toolOperationBlocks(textsanitize.Safe(segment.text, false))
		if receipt != nil && !segment.running && !segment.skipped && segment.exit == 0 {
			writer := execSegmentEdits(segment.source)
			var paths []string
			operations = slices.DeleteFunc(operations, func(block activityui.Block) bool {
				edit := slices.Contains([]string{"Edit", "Create", "Delete", "Move"}, block.Verb)
				if path := activityui.EditPath(block); edit && path != "" {
					paths = append(paths, "`"+path+"`")
				}
				return edit || writer && block.Verb == "Run"
			})
			// The capture replaces edit intent, never the host's output.
			hasOutput := len(segment.tail) > 0 || segment.omit > 0
			if segment.output != nil {
				output := segment.output.View()
				hasOutput = hasOutput || len(output.Lines) > 0 || output.Dropped > 0 || output.Released
			}
			if len(operations) == 0 && hasOutput {
				// Its own row keeps output clicks on the output, not the diff.
				operations = []activityui.Block{edited(paths, segment.source)}
			}
		}
		for i := range operations {
			operations[i].Running, operations[i].Skipped = segment.running, segment.skipped
			if block := &operations[i]; !segment.running && strings.HasSuffix(block.EditSource, " (requested)") {
				// A segment boundary confirms execution, not a file change.
				// Captured evidence alone replaces intent with applied counts.
				status := "ran"
				if segment.skipped {
					status = "skipped"
				} else if segment.exit != 0 {
					status = "failed"
				}
				block.EditSource = strings.TrimSuffix(block.EditSource, " (requested)")
				block.Label = strings.TrimSuffix(block.Label, " (requested)")
				block.EditOutcome = status
			}
		}
		if len(operations) == 0 {
			continue
		}
		last := &operations[len(operations)-1]
		last.ExitCode, last.Segment = segment.exit, true
		last.Exited = !segment.running && !segment.skipped
		if !segment.skipped {
			last.Started, last.Ended = segment.timing.Started, segment.timing.Ended
			last.Duration = time.Duration(segment.timing.ElapsedNS)
		}
		last.Tail, last.TailOmitted, last.Output = segment.tail, segment.omit, segment.output
		last.Changes = commitChanges(segment.changes, segment.commit)
		blocks = append(blocks, operations...)
	}
	singleOutput := len(entry.native.segments) == 1 && entry.native.segments[0].output == nil && entry.native.output != nil
	hasCombinedOutput := singleOutput || len(entry.outputTail) > 0 || entry.outputOmit > 0
	if len(blocks) == 0 && entry.native.output != nil {
		output := entry.native.output.View()
		hasCombinedOutput = hasCombinedOutput || len(output.Lines) > 0 || output.Dropped > 0 || output.Released
	}
	if hasCombinedOutput {
		// Grouped sibling edits may have no remaining intent rows, while
		// terminal/lossy tracking still supplies invocation-level output.
		if len(blocks) == 0 && receipt != nil {
			blocks = append(blocks, edited(nil, command))
		} else if len(blocks) == 0 {
			blocks = append(blocks, activityui.Block{Kind: "op", Verb: "Run", Code: textsanitize.Safe(command, false), Lang: "bash", Fenced: true})
		}
		for i := len(blocks) - 1; i >= 0; i-- {
			if !blocks[i].Skipped {
				blocks[i].Tail, blocks[i].TailOmitted, blocks[i].Output = entry.outputTail, entry.outputOmit, entry.native.output
				blocks[i].Changes = commitChanges(entry.native.changes, entry.native.commit)
				break
			}
		}
	}
	blocks = activityui.FoldStages(blocks)
	for i := range blocks {
		blocks[i].Collapsed = (entry.native.collapsed || blocks[i].ReadOutput()) && blocks[i].Collapsible()
	}
	return activityui.GroupOperations(blocks)
}

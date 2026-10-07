package router

import (
	"cmp"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/yusing/mekugi/internal/livediff"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
	"mvdan.cc/sh/v3/syntax"
)

// Receipt summary of tool-managed files: count, sample paths, optional source.
var liveActivityManagedFiles = regexp.MustCompile(`^\+ (\d+) tool-managed files \((.*)\)(?: · (.+))?$`)

func appServerDuration(item appServerItem) time.Duration {
	if item.DurationMS == nil {
		return 0
	}
	return time.Duration(*item.DurationMS) * time.Millisecond
}

// The host duration supports a single command or an explicitly combined output
// view, never individual rows reconstructed from a command list.
func setCommandTiming(blocks []activityui.Block, entry activityPaneEntry) {
	if entry.native == nil {
		return
	}
	compound := commandTimingIsCompound(blocks, entry)
	for i := range blocks {
		block := &blocks[i]
		if (block.Kind != "op" && block.Kind != "reads") || block.Skipped || compound && !block.BatchExit {
			continue
		}
		block.Duration, block.Started, block.Ended = entry.native.duration, entry.native.commandStarted, entry.native.commandEnded
		block.NotificationTiming = true
		if entry.native.running && block.Started.IsZero() {
			block.Started = entry.Observed
		}
	}
}

func commandTimingIsCompound(blocks []activityui.Block, entry activityPaneEntry) bool {
	command := entry.native.command
	if script, ok := appServerShellScript(command); ok {
		command = script
	}
	compound := len(blocks) > 1 || len(entry.native.segments) > 0
	if program, err := syntax.NewParser().Parse(strings.NewReader(command), ""); err == nil {
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
		if n := entry.native; entry.Kind == "tool" && n != nil && n.command != "" && len(n.segments) == 0 &&
			(n.running || n.duration > 0) && commandTimingIsCompound(blocks, entry) &&
			!slices.ContainsFunc(blocks, func(b activityui.Block) bool { return b.BatchExit }) {
			batch := activityui.Block{Kind: "op", Verb: "Run", Label: "shell batch", BatchExit: true, Running: n.running, Output: n.output, Tail: entry.outputTail, TailOmitted: entry.outputOmit, Collapsed: n.collapsed}
			for i := range blocks {
				blocks[i].Output, blocks[i].Tail, blocks[i].TailOmitted = nil, nil, 0
			}
			blocks = append(blocks, batch)
		}
		setCommandTiming(blocks, entry)
		if entry.Kind == "tool" && entry.native != nil && entry.native.workdir != "" && len(blocks) > 0 {
			// Every row keeps the directory for merging; one label per invocation.
			for i := range blocks {
				blocks[i].Workdir = livediff.Safe(entry.native.workdir, false)
			}
			blocks[0].ShowWorkdir = true
		}
	}()
	text := livediff.Safe(entry.Text, false)
	source := activityui.MarkdownSource(entry.Text)
	switch entry.Kind {
	case "journal_card":
		return []activityui.Block{{Kind: "summary", Label: "Journal", Body: text, Collapsed: true}}
	case "journal_event":
		return []activityui.Block{{Kind: "progress", Body: text}}
	case "text":
		if body, ok := strings.CutPrefix(strings.TrimSpace(source), "Journal\n"); ok {
			return []activityui.Block{{Kind: "journal", Body: strings.TrimSpace(body)}}
		}
	case "reply":
		// Direction comes from the delivered message, never from its text.
		if entry.message != nil {
			return []activityui.Block{{Kind: "message", From: entry.message.from, To: entry.message.to, Owner: entry.Agent, Body: strings.Trim(livediff.Safe(entry.message.text, false), "\n")}}
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
			return []activityui.Block{*entry.native.wait}
		}
		if entry.native != nil && entry.native.phase == "task" {
			return []activityui.Block{{Kind: "progress", Label: text, Body: text}}
		}
		return []activityui.Block{{Kind: "progress", Body: text}}
	case "assignment":
		if entry.assignment != nil {
			return []activityui.Block{{Kind: "message", From: entry.assignment.from, To: entry.assignment.to, Owner: entry.Agent, Body: livediff.Safe(entry.assignment.text, false)}}
		}
	case "reasoning":
		blocks := activityui.ReasoningSections(source)
		for i := range blocks {
			block := &blocks[i]
			if entry.native != nil {
				block.Live = entry.native.phase == "summary" || entry.native.phase == "item/started"
				block.Collapsed = entry.native.collapsed && block.Collapsible()
				// Duration belongs to the item, not to each detected section.
				if i == len(blocks)-1 && entry.native.thought >= time.Second {
					block.Elapsed = liveActivityAge(entry.native.thought)
				}
			}
		}
		return blocks
	case "native_journal":
		if entry.journal != nil {
			journal := &activityui.Journal{}
			items := entry.journalItems
			if len(items) == 0 {
				items = []journalItem{*entry.journal}
			}
			questions := make(map[string]int)
			for _, item := range items {
				index, found := questions[item.Question]
				if item.Question == "" || !found {
					index = len(journal.Groups)
					journal.Groups = append(journal.Groups, activityui.AnswerGroup{Question: livediff.Safe(item.Question, false)})
					questions[item.Question] = index
				}
				journal.Groups[index].Answers = append(journal.Groups[index].Answers, activityui.Answer{ID: item.ID, Text: livediff.Safe(item.Text, false)})
			}
			return []activityui.Block{{Kind: "final", Body: text, Journal: journal}}
		}
	case "command":
		if entry.native != nil {
			return []activityui.Block{{Kind: "op", Verb: "Run", Label: livediff.Safe(entry.native.status, false), Code: livediff.Safe(entry.native.command, false), Lang: "bash", Fenced: true}}
		}
	case "final":
		journal, _ := activityui.ParseJournal(text)
		return []activityui.Block{{Kind: "final", Body: source, Journal: journal, Owner: entry.Agent}}
	case "start":
		if entry.start != nil {
			block := activityui.Block{Kind: "start", Label: livediff.Safe(entry.start.label(), false)}
			if entry.assignment != nil {
				block.From, block.To = entry.assignment.from, entry.assignment.to
				block.Body = livediff.Safe(entry.assignment.text, false)
			}
			return []activityui.Block{block}
		}
	case "error":
		if entry.ErrorDetail != "" {
			text = livediff.Safe(entry.ErrorDetail, false)
		}
		return []activityui.Block{{Kind: "error", Body: text, Label: entry.Agent}}
	case "compaction":
		return []activityui.Block{{Kind: "compaction", Body: text}}
	case "tool":
		if entry.native != nil && len(entry.native.segments) > 0 {
			return commandSegmentBlocks(entry)
		}
		blocks := activityui.FoldStages(toolOperationBlocks(text))
		if len(blocks) == 1 && blocks[0].Verb == "Search" && entry.native != nil {
			blocks[0].Results = entry.native.searchResults
		}
		if entry.native != nil && len(blocks) > 0 {
			blocks[len(blocks)-1].Output = entry.native.output
			if entry.native.status == "failed" {
				last := &blocks[len(blocks)-1]
				last.Label = strings.TrimSpace(last.Label + " · failed")
			}
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

// toolOperationBlocks parses a tool entry's operations, one per paragraph,
// merging adjacent reads.
func toolOperationBlocks(text string) []activityui.Block {
	var blocks []activityui.Block
	previousSource := ""
	for _, paragraph := range activityui.Paragraphs(text) {
		block := activityui.ParseOperation(paragraph)
		if match := liveActivityManagedFiles.FindStringSubmatch(paragraph); match != nil {
			// A receipt's tool-managed files are confirmed edits of its source.
			source := cmp.Or(match[3], previousSource)
			block = activityui.Block{Kind: "op", Verb: "Edit", Label: match[1] + " tool-managed files (" + match[2] + ")"}
			if source != "" {
				block.Label += " · " + source
				block.EditSource, block.EditHeader = source, source != previousSource
			}
		}
		switch block.Verb {
		case "Create", "Edit", "Delete", "Move":
			if source, ok := strings.CutPrefix(block.Label, "· "); ok {
				// A targetless edit still belongs to its invocation, but must
				// retain the fact that its paths could not be resolved.
				block.Label = "paths unavailable · " + source
			}
			if _, source, ok := strings.CutLast(block.Label, " · "); ok {
				block.EditSource = source
				block.EditHeader = source != previousSource
			}
			if block.Fenced && block.Lang == "diff" {
				block.Code, block.Lang, block.Fenced = "", "", false
			}
		}
		previousSource = block.EditSource
		blocks = append(blocks, block)
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
	blocks := toolOperationBlocks(livediff.Safe(text, false))
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

// commandExitBlocks keeps a combined shell failure separate from classified
// operations: without a complete segment report none owns the batch's exit.
func commandExitBlocks(blocks []activityui.Block, code int, tail []string, omitted int) []activityui.Block {
	last, count, batch := -1, 0, -1
	var output *activityui.Output
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
	blocks[last].ExitCode = code
	blocks[last].Tail, blocks[last].TailOmitted, blocks[last].Output = tail, omitted, output
	blocks[last].Collapsed = false
	return blocks
}

// commandSegmentBlocks shows each segment of a tracked command as its own
// operations, with that segment's state, exit status, and output after its
// last operation. Without per-segment output, the host's combined output
// follows the last segment shown.
func commandSegmentBlocks(entry activityPaneEntry) []activityui.Block {
	var blocks []activityui.Block
	receipt := entry.native.capturedEdit
	if receipt != nil && len(receipt.calls) > 0 && receipt.calls[0] == entry.native.item {
		// Counts belong to the capture as a whole, not any individual segment.
		blocks = append(blocks, toolOperationBlocks(livediff.Safe(receipt.text, false))...)
	}
	// Classify decoration in the full script, not as a standalone segment.
	// Keep failed headings visible, and retain every segment for replay/output.
	command := entry.native.command
	if script, ok := appServerShellScript(command); ok {
		command = script
	}
	_, classified := toolActivityReads(command)
	for _, segment := range entry.native.segments {
		if classified && !segment.skipped && segment.exit == 0 {
			if program, err := syntax.NewParser().Parse(strings.NewReader(segment.source), ""); err == nil && len(program.Stmts) == 1 && toolActivityReadSeparator(program.Stmts[0]) {
				continue
			}
		}
		operations := toolOperationBlocks(livediff.Safe(segment.text, false))
		if receipt != nil && !segment.running && !segment.skipped && segment.exit == 0 {
			operations = slices.DeleteFunc(operations, func(block activityui.Block) bool {
				return slices.Contains([]string{"Edit", "Create", "Delete", "Move"}, block.Verb)
			})
			// The capture replaces edit intent, never the host's output. Keep
			// an output-bearing edit-only segment as its real shell command.
			hasOutput := len(segment.tail) > 0 || segment.omit > 0
			if segment.output != nil {
				output := segment.output.View()
				hasOutput = hasOutput || len(output.Lines) > 0 || output.Dropped > 0 || output.Released
			}
			if len(operations) == 0 && hasOutput {
				operations = []activityui.Block{{Kind: "op", Verb: "Run", Code: livediff.Safe(segment.source, false), Lang: "bash", Fenced: true}}
			}
		}
		for i := range operations {
			operations[i].Running, operations[i].Skipped = segment.running, segment.skipped
			if block := &operations[i]; !segment.running && strings.HasSuffix(block.EditSource, " (requested)") {
				status := "completed"
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
		if !segment.skipped {
			last.Started, last.Ended = segment.timing.Started, segment.timing.Ended
			last.Duration = time.Duration(segment.timing.ElapsedNS)
		}
		last.Tail, last.TailOmitted, last.Output = segment.tail, segment.omit, segment.output
		last.Changes = commitChanges(segment.changes, segment.commit)
		blocks = append(blocks, operations...)
	}
	hasCombinedOutput := len(entry.outputTail) > 0 || entry.outputOmit > 0
	if len(blocks) == 0 && entry.native.output != nil {
		output := entry.native.output.View()
		hasCombinedOutput = hasCombinedOutput || len(output.Lines) > 0 || output.Dropped > 0 || output.Released
	}
	if hasCombinedOutput {
		// Grouped sibling edits may have no remaining intent rows, while
		// terminal/lossy tracking still supplies invocation-level output.
		if len(blocks) == 0 {
			blocks = append(blocks, activityui.Block{Kind: "op", Verb: "Run", Code: livediff.Safe(command, false), Lang: "bash", Fenced: true})
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

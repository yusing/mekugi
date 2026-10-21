package presentation

import (
	"cmp"
	"slices"
	"strings"
	"time"

	"github.com/yusing/mekugi/libraries/textsanitize"
)

// ItemContent admits one original observation for source interpretation. It is
// transient input, not a second feed, and contains no renderer state or handles.
type ItemContent struct {
	Facts
	Command             Command
	Details             *ItemDetails
	Message, Assignment *DirectedContent
	StartLabel          *string
	ErrorDetail         string
}

type DirectedContent struct{ From, To, Text string }

type ItemDetails struct {
	CommandText, Status, Timeout, Workdir string
	ReasoningLive, WaitLive               bool
	Output                                int
	SearchResults                         *int
	Questions                             []Question
	Attachments                           []Value
	Wait                                  *Value
	Hook                                  *HookDetails
	Thought                               time.Duration
}

// Source: apps/mekugi/internal/router/live_activity_blocks.go:35:175@35fd8de4.
func (entry ItemContent) Values() (blocks []Value) {
	command := entry.Command
	defer func() {
		defer func() { Identify(blocks, entry.Seq) }()
		// An unsupported batch has one host clock, not a clock per classified
		// operation. Keep that total visible and its output on one explicit row.
		if n := entry.Details; entry.Kind == "tool" && n != nil && n.CommandText != "" &&
			(len(entry.Command.Segments) == 0 && (entry.Command.Running || entry.Command.Duration > 0) && command.Compound(blocks) || entry.Command.Approval != "" && len(blocks) > 1) &&
			!slices.ContainsFunc(blocks, func(b Value) bool { return b.BatchExit }) {
			batch := Value{Kind: "op", Verb: "Run", Label: "shell batch", BatchExit: true, Running: entry.Command.Running, Output: n.Output, Tail: entry.Command.Tail, TailOmitted: entry.Command.TailOmitted}
			if len(entry.Command.Segments) == 0 {
				for i := range blocks {
					blocks[i].Output, blocks[i].Tail, blocks[i].TailOmitted = 0, nil, 0
				}
			} else {
				batch.Output, batch.Tail, batch.TailOmitted = 0, nil, 0
			}
			blocks = append(blocks, batch)
		}
		if n := entry.Details; n != nil && n.Timeout != "" {
			for i := range blocks {
				if !slices.Contains(blocks[i].Timeouts, n.Timeout) {
					blocks[i].Timeouts = append(slices.Clone(blocks[i].Timeouts), n.Timeout)
				}
			}
		}
		if entry.Details != nil {
			command.ApplyFacts(blocks)
		}
		if entry.Details != nil && len(blocks) > 0 {
			ApplyApproval(blocks, entry.Command.Approval)
		}
		if entry.Kind == "tool" && entry.Details != nil && entry.Details.Workdir != "" && len(blocks) > 0 {
			// Every row keeps the directory for merging; one label per invocation.
			for i := range blocks {
				blocks[i].Workdir = textsanitize.Safe(entry.Details.Workdir, false)
			}
			blocks[0].ShowWorkdir = true
		}
	}()
	text := textsanitize.Safe(entry.Text, false)
	source := textsanitize.MarkdownSource(entry.Text)
	switch entry.Kind {
	case "hook":
		n := entry.Details
		if n == nil || n.Hook == nil {
			return nil
		}
		return []Value{{Kind: "op", Verb: "Run", Hook: new(*n.Hook), Label: text, Running: entry.Command.Running,
			Output: n.Output, Tail: entry.Command.Tail, TailOmitted: entry.Command.TailOmitted}}
	case "reply":
		// Direction comes from the delivered message, never from its text.
		if entry.Message != nil {
			return []Value{{Kind: "message", From: entry.Message.From, To: entry.Message.To, Owner: entry.Agent, Body: textsanitize.MarkdownSource(entry.Message.Text), ReplySummary: entry.replySummary(entry.Message.Text)}}
		}
	case "question":
		label, body, _ := strings.Cut(text, "\n")
		block := Value{Kind: "op", Verb: "Asked", Label: label, Body: body}
		if entry.Details != nil {
			block.Questions = slices.Clone(entry.Details.Questions)
		}
		return []Value{block}
	case "attachments":
		if entry.Details != nil {
			return CloneValues(entry.Details.Attachments)
		}
	case "progress":
		if entry.Details != nil && entry.Details.Wait != nil {
			block := *entry.Details.Wait
			block.Live = entry.Details.WaitLive
			return []Value{block}
		}
		return []Value{{Kind: "progress", Body: text}}
	case "assignment":
		if entry.Assignment != nil {
			return []Value{{Kind: "message", From: entry.Assignment.From, To: entry.Assignment.To, Owner: entry.Agent, Body: textsanitize.Safe(entry.Assignment.Text, false)}}
		}
	case "reasoning":
		blocks := ReasoningSections(source)
		for i := range blocks {
			block := &blocks[i]
			if entry.Details != nil {
				block.Live = i == len(blocks)-1 && entry.Details.ReasoningLive
				// Duration belongs to the item, not to each detected section.
				if i == len(blocks)-1 {
					block.Thought = entry.Details.Thought
				}
			}
		}
		return blocks
	case "command":
		if entry.Details != nil {
			return []Value{{Kind: "op", Verb: "Run", Label: textsanitize.Safe(entry.Details.Status, false), Code: textsanitize.Safe(entry.Details.CommandText, false), Lang: "bash", Fenced: true}}
		}
	case "final":
		return []Value{{Kind: "final", Body: source, Owner: entry.Agent, ReplySummary: entry.replySummary(entry.Text)}}
	case "start":
		if entry.StartLabel != nil {
			block := Value{Kind: "start", Label: textsanitize.Safe(*entry.StartLabel, false)}
			if entry.Assignment != nil {
				block.From, block.To = entry.Assignment.From, entry.Assignment.To
				block.Body = textsanitize.Safe(entry.Assignment.Text, false)
			}
			return []Value{block}
		}
	case "error":
		if entry.ErrorDetail != "" {
			text = textsanitize.Safe(entry.ErrorDetail, false)
		}
		return []Value{{Kind: "error", Body: text, Label: entry.Agent}}
	case "compaction":
		return []Value{{Kind: "compaction", Body: text}}
	case "tool":
		if command.UserShell {
			blocks := []Value{{Kind: "op", Verb: "Run", UserShell: true,
				Code: textsanitize.Safe(command.Display, false), Lang: "bash", Fenced: true,
				Running: command.Running, Output: outputID(command.Output, 1),
				Tail: command.Tail, TailOmitted: command.TailOmitted}}
			return append(blocks, Operations(textsanitize.Safe(command.CaptureText, false))...)
		}
		if entry.Details != nil && len(entry.Command.Segments) > 0 {
			command.Results = entry.Details.SearchResults
			return GroupOperations(command.SegmentValues(false))
		}
		blocks := FoldStages(Operations(text))
		if len(blocks) == 1 && slices.Contains([]string{"Search", "Web Search", "List", "Inspect"}, blocks[0].Verb) && entry.Details != nil {
			if entry.Details.SearchResults != nil {
				blocks[0].Results = new(*entry.Details.SearchResults)
			}
		}
		if entry.Details != nil && len(blocks) > 0 {
			blocks[len(blocks)-1].Output = entry.Details.Output
		}
		if entry.Details != nil && (entry.Command.Running || len(entry.Command.Tail) > 0) {
			// The host provides one stream for the whole invocation. Place it
			// after the final displayed operation, not an earlier Run row.
			for i := range blocks {
				blocks[i].Running = entry.Command.Running
			}
			if len(blocks) > 0 {
				last := &blocks[len(blocks)-1]
				last.Tail, last.TailOmitted = entry.Command.Tail, entry.Command.TailOmitted
				last.Changes = entry.Command.Changes
			}
		}
		return GroupOperations(blocks)
	}
	return []Value{{Kind: "text", Body: source}}
}

// Source: apps/mekugi/internal/router/live_activity_blocks.go:184:242@35fd8de4.
// RetainBatchResult preserves the outcome and output across receipt reparses,
// replacing a synthesized timing row rather than duplicating the invocation.
func RetainBatchResult(blocks []Value, batch Value) []Value {
	if at := slices.IndexFunc(blocks, func(b Value) bool { return b.BatchExit }); at >= 0 {
		blocks[at] = batch
		return blocks
	}
	return append(blocks, batch)
}

// ApplyApproval puts a compound invocation's state on its aggregate row.
func ApplyApproval(blocks []Value, outcome string) {
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

// CommandExit keeps a combined shell failure separate from classified
// operations: without a complete segment report none owns the batch's exit.
func CommandExit(blocks []Value, code int, tail []string, omitted int) []Value {
	last, count, batch := -1, 0, -1
	var output int
	for i, block := range blocks {
		if block.UserShell {
			blocks[i].ExitCode, blocks[i].Exited = code, true
			blocks[i].Tail, blocks[i].TailOmitted = tail, omitted
			return blocks
		}
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
			blocks = append(blocks, Value{Kind: "op", Verb: "Run", Label: "shell batch", BatchExit: true})
		}
		// Any streamed tail was combined too; move it to the batch result.
		for i := range blocks {
			blocks[i].Tail, blocks[i].TailOmitted, blocks[i].Output = nil, 0, 0
		}
		last = batch
	}
	blocks[last].ExitCode, blocks[last].Exited = code, true
	blocks[last].Tail, blocks[last].TailOmitted, blocks[last].Output = tail, omitted, output
	return blocks
}

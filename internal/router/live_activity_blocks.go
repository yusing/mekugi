package router

import (
	"cmp"
	"regexp"
	"strings"

	"github.com/yusing/mekugi/internal/livediff"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
)

// Receipt summary of tool-managed files: count, sample paths, optional source.
var liveActivityManagedFiles = regexp.MustCompile(`^\+ (\d+) tool-managed files \((.*)\)(?: · (.+))?$`)

func parseLiveActivity(entry activityPaneEntry) []activityui.Block {
	text := livediff.Safe(entry.Text, false)
	// Only the router writes reply envelopes; child-authored text that looks
	// like one stays plain, so it cannot pose as another agent's message.
	if entry.Kind == "reply" {
		if from, to, headline, body, ok := activityui.ParseEnvelope(text); ok {
			return []activityui.Block{{Kind: "message", From: from, To: to, Owner: entry.Agent, Verb: headline, Body: body}}
		}
	}
	switch entry.Kind {
	case "assignment":
		if entry.assignment != nil {
			return []activityui.Block{{Kind: "message", From: entry.assignment.from, To: entry.assignment.to, Owner: entry.Agent, Body: livediff.Safe(entry.assignment.text, false)}}
		}
	case "reasoning":
		block := activityui.Block{Kind: "summary", Body: text}
		if entry.native != nil {
			block.Live = entry.native.phase == "summary"
			block.Done = entry.native.done
			if entry.native.thought > 0 {
				block.Elapsed = liveActivityAge(entry.native.thought)
			}
		}
		return []activityui.Block{block}
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
		return []activityui.Block{{Kind: "final", Body: text, Journal: journal, Owner: entry.Agent}}
	case "start":
		if strings.HasPrefix(text, "Started") {
			block := activityui.ParseStart(text)
			if entry.assignment != nil {
				block.From, block.To = entry.assignment.from, entry.assignment.to
				block.Body = livediff.Safe(entry.assignment.text, false)
			}
			return []activityui.Block{block}
		}
	case "error":
		return []activityui.Block{{Kind: "error", Body: text}}
	case "compaction":
		return []activityui.Block{{Kind: "compaction", Body: text}}
	case "output_filter":
		if entry.Filter != nil {
			return []activityui.Block{{Kind: "filter", Body: text}}
		}
	case "tool":
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
		if len(blocks) == 1 && blocks[0].Verb == "Search" && entry.native != nil {
			blocks[0].Results = entry.native.searchResults
		}
		return activityui.GroupOperations(blocks)
	}
	return []activityui.Block{{Kind: "text", Body: text}}
}

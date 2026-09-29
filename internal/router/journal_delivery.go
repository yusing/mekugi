package router

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"

	"strings"

	responseevents "github.com/yusing/mekugi/internal/responses"
)

type journalResultWindow struct {
	sequence, changes, count uint64
}

type journalDelivery struct {
	tree      bool
	sequence  uint64
	thread    string
	revisions map[string]uint64
	terminal  bool
}

func journalItemText(item journalItem) string {
	if item.Question == "" {
		return item.Text
	}
	return "**Question:**\n\n" + item.Question + "\n\n**Answer:**\n\n" + item.Text
}

func indentJournalText(text, indent string) string {
	// Normalize only the rendering copy, leaving stored questions and answers intact.
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	return indent + strings.ReplaceAll(text, "\n", "\n"+indent)
}

func writeSingleJournalItem(text *strings.Builder, item journalItem) {
	text.WriteString(" (" + commentaryCode(item.ID) + ")\n\n" + indentJournalText(journalItemText(item), ""))
}

func writeJournalItems(text *strings.Builder, items []journalItem) {
	questions := make(map[string]int)
	var groups [][]journalItem
	for _, item := range items {
		index, seen := questions[item.Question]
		if item.Question == "" || !seen {
			index = len(groups)
			groups = append(groups, nil)
			if item.Question != "" {
				questions[item.Question] = index
			}
		}
		groups[index] = append(groups[index], item)
	}
	for index, group := range groups {
		question := group[0].Question
		if index > 0 && (question != "" || groups[index-1][0].Question != "") {
			text.WriteString("\n---\n")
		}
		if question != "" {
			label := "**Answer:**"
			if len(group) > 1 {
				label = "**Answers:**"
			}
			text.WriteString("\n\n**Question:**\n\n" + indentJournalText(question, "") + "\n\n" + label + "\n")
		}
		for _, item := range group {
			text.WriteString("\n- " + commentaryCode(item.ID) + "\n\n" + indentJournalText(item.Text, "  ") + "\n")
		}
	}
}

func journalUpdateText(author, id, text string) string {
	heading := "Journal update"
	if author != "" {
		heading += " " + commentaryCode(author)
	}
	return heading + " (" + commentaryCode(id) + ")\n" + text
}

func (t *mekugiResponseTransform) prepareJournalDelivery(terminal bool) ([]map[string]json.RawMessage, error) {
	if !t.journalActive {
		return nil, nil
	}
	// Native child completion delivers its own result. Main flushes only its
	// own journal, never repeats child results.
	childTerminal := terminal && t.subagentTurn
	terminal = terminal && !t.subagentTurn
	// Journal writes atomically replace the file. Avoid decoding an unchanged
	// multi-megabyte journal for every provider delta when no live notices remain.
	// A concurrent change is observed at the next event; terminals always refresh.
	if !terminal && !childTerminal && t.journalQuietFile != nil && t.proxy.replayStore != nil {
		info, err := os.Lstat(filepath.Join(t.proxy.replayStore.directory, journalFilename(t.directory, t.shellThreadID)))
		if err == nil && os.SameFile(info, t.journalQuietFile) &&
			info.Size() == t.journalQuietFile.Size() && info.ModTime() == t.journalQuietFile.ModTime() {
			return nil, nil
		}
	}
	t.journalQuietFile = nil
	release, err := t.proxy.journals.lockDelivery(t.ctx, t.proxy.replayStore)
	if err != nil {
		return nil, err
	}
	t.journalDeliveryRelease = release
	var changes string
	var journal threadJournal
	releaseState, err := t.proxy.journals.lockState(t.ctx)
	if err != nil {
		t.ReleaseDelivery()
		return nil, err
	}
	read := func() error {
		var exists bool
		if t.proxy.replayStore != nil {
			if !terminal {
				t.journalQuietFile, _ = os.Lstat(filepath.Join(t.proxy.replayStore.directory, journalFilename(t.directory, t.shellThreadID)))
			}
			journal, exists, err = readThreadJournal(t.proxy.replayStore, t.directory, t.shellThreadID)
		} else {
			journal, exists = t.proxy.journals.memory[journalKey(t.directory, t.shellThreadID)]
			journal = journal.clone()
		}
		if err != nil {
			return err
		}
		if !exists {
			journal = threadJournal{Author: "/root"}
			if t.commentaryAuthor != "" {
				journal.Author = t.commentaryAuthor
			}
		}
		if childTerminal {
			var changeSeq uint64
			changes, changeSeq = t.proxy.replayStore.childJournalChangesSince(t.ctx, t.directory, t.shellThreadID, journal.ResultChangeSeq, journal.ResultCount != 0)
			t.journalResultWindow = &journalResultWindow{sequence: journal.Sequence, changes: changeSeq, count: journal.ResultCount + 1}
		}
		return nil
	}
	if t.proxy.replayStore != nil {
		err = t.proxy.replayStore.locked(t.ctx, read)
	} else {
		err = read()
	}
	releaseState()
	if err != nil {
		t.ReleaseDelivery()
		return nil, err
	}
	if sink := t.nativeJournal(); sink != nil {
		if terminal {
			snapshot := journal.clone()
			t.journalNativeTerminal = &snapshot
		} else {
			sink.publish(journal, false)
		}
		t.ReleaseDelivery()
		return nil, nil
	}
	if childTerminal {
		t.journalNewCount, t.journalFlushedCount = 0, 0
		for _, item := range journal.Items {
			if item.Flushed {
				t.journalFlushedCount++
			} else {
				t.journalNewCount++
			}
		}
		var text strings.Builder
		text.WriteString("Journal result")
		if journal.Author != "" {
			text.WriteString(" " + commentaryCode(journal.Author))
		}
		entries := 0
		for _, item := range journal.Items {
			if item.Updated <= journal.ResultSeq {
				continue
			}
			entries++
			if item.TerminalOnly {
				text.WriteString("\n\n" + indentJournalText(item.Text, ""))
			} else {
				text.WriteString("\n- " + strings.TrimPrefix(indentJournalText(item.Text, "  "), "  "))
			}
		}
		if entries == 0 {
			text.WriteString("\nNo new journal entries.")
		}

		text.WriteString(changes)
		if journal.TreeAuthored {
			t.journalChildResult = journalTurnCard(journal, journal.ResultSeq, true) + changes
		} else {
			t.journalChildResult = text.String()
		}
		if len(t.journalChildResult) > maxJournalFlushBytes {
			t.ReleaseDelivery()
			return nil, errors.New("child journal result exceeds terminal capacity")
		}
	}
	if journal.TreeAuthored && !childTerminal {
		return t.prepareTreeDelivery(journal, terminal)
	}
	for _, item := range journal.Items {
		if item.ReportNow && !item.Reported && len(journalUpdateText(journal.Author, item.ID, journalItemText(item))) <= maxCommentaryPublicationBytes-t.journalLiveBytes {
			t.journalQuietFile = nil
			break
		}
	}
	for _, retraction := range journal.Retractions {
		if len(journalUpdateText(journal.Author, retraction.ID, "Retracted.")) <= maxCommentaryPublicationBytes-t.journalLiveBytes {
			t.journalQuietFile = nil
			break
		}
	}
	var messages []map[string]json.RawMessage
	prepared := make(map[string]journalDelivery)
	deliveryThread := t.shellThreadID
	emit := func(text string, revisions map[string]uint64, source string) {
		id := commentaryMessageID("journal\x00" + t.directory + "\x00" + deliveryThread + "\x00" + source)
		message := assistantCommentaryMessage(id, text)
		prepared[id] = journalDelivery{thread: deliveryThread, revisions: revisions, terminal: terminal, sequence: journal.Sequence}
		traceSource := "report_now"
		if terminal {
			message["phase"] = mustMarshalJSON("final_answer")
			traceSource = "terminal_flush"
		}
		t.featureTrace.record("journal", traceSource, "render", "prepared", "", id)

		messages = append(messages, message)
	}
	emitRetractions := func(journal threadJournal) {
		for _, retraction := range journal.Retractions {
			text := journalUpdateText(journal.Author, retraction.ID, "Retracted.")
			if !terminal && len(text) > maxCommentaryPublicationBytes-t.journalLiveBytes {
				continue
			}
			emit(text, map[string]uint64{retraction.ID: retraction.Sequence}, fmt.Sprintf("retract:%d", retraction.Sequence))
			if !terminal {
				t.journalLiveBytes += len(text)
			}
		}
	}
	if !terminal {
		emitRetractions(journal)
	}
	if terminal {
		t.journalNewCount, t.journalFlushedCount = 0, 0
		deliveryThread = journal.Thread
		if deliveryThread == "" {
			deliveryThread = t.shellThreadID
		}
		emitRetractions(journal)
		var text strings.Builder
		text.WriteString("Journal flush")
		if journal.Author != "" {
			text.WriteString(" " + commentaryCode(journal.Author))
		}
		revisions := make(map[string]uint64)
		var items []journalItem
		flushed := 0
		for _, item := range journal.Items {
			if item.Flushed {
				flushed++
				continue
			}
			items = append(items, item)
			revisions[item.ID] = item.Updated
		}
		if len(items) == 1 {
			writeSingleJournalItem(&text, items[0])
		} else {
			writeJournalItems(&text, items)
		}
		t.journalNewCount += len(revisions)
		t.journalFlushedCount += flushed
		if len(revisions) != 0 {
			if text.Len() > maxJournalFlushBytes {
				t.ReleaseDelivery()
				return nil, fmt.Errorf("journal flush exceeds terminal capacity")
			}
			emit(text.String(), revisions, fmt.Sprintf("flush:%d", journal.Sequence))
		}
	} else {
		for _, item := range journal.Items {
			if item.Reported || !item.ReportNow {
				continue
			}
			text := journalUpdateText(journal.Author, item.ID, journalItemText(item))
			if len(text) > maxCommentaryPublicationBytes-t.journalLiveBytes {
				continue
			}
			emit(text, map[string]uint64{item.ID: item.Updated}, fmt.Sprintf("item:%d", item.Updated))
			t.journalLiveBytes += len(text)
		}
	}
	// Validate the journal before retaining any message IDs or delivery entries.
	if len(messages) != 0 {
		if len(t.retainCommentary(messages...)) == 0 {
			t.ReleaseDelivery()
			if terminal {
				return nil, errors.New("cannot retain journal terminal delivery")
			}
			return nil, nil
		}
		if t.journalDeliveries == nil {
			t.journalDeliveries = make(map[string]journalDelivery)
		}
		maps.Copy(t.journalDeliveries, prepared)
	}
	if len(messages) == 0 {
		t.ReleaseDelivery()
	}
	return messages, nil
}

// The transport confirms only after a successful downstream write/flush. The
// delivery lease remains held until the whole batch is finished or abandoned.
func (t *mekugiResponseTransform) Delivered(payload []byte) {
	var envelope struct {
		Type     string                       `json:"type"`
		Status   string                       `json:"status"`
		Item     map[string]json.RawMessage   `json:"item"`
		Output   []map[string]json.RawMessage `json:"output"`
		Response struct {
			Output []map[string]json.RawMessage `json:"output"`
		} `json:"response"`
	}
	if json.Unmarshal(payload, &envelope) != nil {
		return
	}
	if (t.journalTerminalReady() || t.liveDiffCompletionReady) && (envelope.Type == "response.completed" || envelope.Status == "completed") {
		t.storageIdle = true
		t.finishLiveDiffTurn()
		if window := t.journalResultWindow; window != nil {
			if err := t.proxy.journals.acknowledgeResult(t.ctx, t.proxy.replayStore, t.directory, t.shellThreadID, window.sequence, window.changes, window.count); err == nil {
				t.journalResultWindow = nil
			}
		}
		if t.journalNativeTerminal != nil && t.journalNativeSink != nil {
			t.journalNativeSink.publish(*t.journalNativeTerminal, true)
			t.journalNativeTerminal = nil
		}
	}
	if len(t.journalDeliveries) == 0 {
		return
	}
	items := append(envelope.Output, envelope.Response.Output...)
	if envelope.Item != nil {
		items = append(items, envelope.Item)
	}
	for _, item := range items {
		id := jsonString(item, "id")
		delivery, ok := t.journalDeliveries[id]
		if !ok {
			continue
		}
		if delivery.tree {
			if err := t.proxy.journals.acknowledgeTree(t.ctx, t.proxy.replayStore, t.directory, delivery.thread, delivery.sequence, delivery.terminal); err == nil {
				delete(t.journalDeliveries, id)
			}
			continue
		}
		if err := t.proxy.journals.acknowledge(t.ctx, t.proxy.replayStore, t.directory, delivery.thread, delivery.revisions, delivery.terminal); err != nil {
			continue
		}
		delete(t.journalDeliveries, id)
	}
}

func commentaryMessageText(item map[string]json.RawMessage) string {
	var content []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(item["content"], &content) != nil {
		return ""
	}
	var text strings.Builder
	for _, part := range content {
		text.WriteString(part.Text)
	}
	return text.String()
}

func releaseResponseDelivery(transformer responseTransformer) {
	if delivery, ok := transformer.(interface{ ReleaseDelivery() }); ok {
		delivery.ReleaseDelivery()
	}
}

func confirmResponseDelivery(transformer responseTransformer, payload []byte) {
	if delivery, ok := transformer.(interface{ Delivered([]byte) }); ok {
		delivery.Delivered(payload)
	}
}

func (chain responseTransformerChain) ReleaseDelivery() {
	releaseResponseDelivery(chain.first)
	releaseResponseDelivery(chain.second)
}

func (chain responseTransformerChain) Delivered(payload []byte) {
	confirmResponseDelivery(chain.first, payload)
	confirmResponseDelivery(chain.second, payload)
}

func (t *mekugiResponseTransform) journalTerminalMessages(response []byte) ([]map[string]json.RawMessage, error) {
	var messages []map[string]json.RawMessage
	var counts tokenUsageReport
	observed := false
	// Natural completion is terminal even with an empty journal. Missing current
	// usage must invalidate prior totals before rendering, not hide the report.
	if !t.subagentTurn {
		if !t.usageObserved {
			t.usageTracker.finish()
		}
		counts, observed = t.completionUsageReport()
	}
	if observed {
		t.proxy.writeTokenMetrics(t.shellThreadID, counts)
	}
	if t.subagentTurn {
		id := commentaryMessageID("journal-summary\x00" + jsonResponseID(response))
		message := assistantCommentaryMessage(id, t.journalChildResult)
		message["phase"] = mustMarshalJSON("final_answer")
		retained := t.retainCommentary(message)
		if len(retained) == 0 {
			return nil, errors.New("cannot retain child journal terminal summary")
		}
		messages = append(messages, retained...)
	}
	return messages, nil
}

func jsonResponseID(response []byte) string {
	var identity struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(response, &identity)
	return identity.ID
}

func (t *mekugiResponseTransform) decorateJournalJSON(payload []byte) ([]byte, error) {
	var response map[string]json.RawMessage
	if err := json.Unmarshal(payload, &response); err != nil {
		return nil, err
	}
	terminal := jsonString(response, "status") == "completed" && t.journalTerminalReady()
	if terminal {
		t.journalResponseID = jsonString(response, "id")
	}
	messages, err := t.prepareJournalDelivery(terminal)
	if err != nil {
		return nil, err
	}
	var output []map[string]json.RawMessage
	if err := decodeJournalOutput(response["output"], &output); err != nil {
		t.ReleaseDelivery()
		return nil, err
	}
	if terminal {
		output = t.withoutNaturalAnswer(output)
		terminalMessages, err := t.journalTerminalMessages(payload)
		if err != nil {
			t.ReleaseDelivery()
			return nil, err
		}
		// Keep the final-answer flush last. Later commentary makes Codex render
		// that final answer again when it receives turn completion.
		if t.subagentTurn {
			messages = append(messages, terminalMessages...)
		} else {
			messages = append(terminalMessages, messages...)
		}
		output = append(output, messages...)
	} else {
		output = append(messages, output...)
	}
	response["output"] = mustMarshalJSON(output)
	return marshalProtocolJSON(response)
}

func (t *mekugiResponseTransform) decorateJournalSSE(original []byte, events [][]byte) ([][]byte, error) {
	if t.journalContinue {
		messages, err := t.prepareJournalDelivery(false)
		if err != nil {
			return nil, err
		}
		var notices [][]byte
		for _, message := range messages {
			notices = append(notices, assistantCommentaryDoneEvent(message))
		}
		return append(notices, events...), nil
	}

	if t.journalTerminal {
		var envelope struct {
			Response json.RawMessage `json:"response"`
		}
		if json.Unmarshal(original, &envelope) == nil {
			t.journalResponseID = jsonResponseID(envelope.Response)
		}
	}
	messages, err := t.prepareJournalDelivery(t.journalTerminal)
	if err != nil {
		return nil, err
	}
	if !t.journalTerminal {
		var notices [][]byte
		for _, message := range messages {
			notices = append(notices, assistantCommentaryDoneEvent(message))
		}
		if len(events) != 0 {
			var event struct {
				Type responseevents.Kind `json:"type"`
			}
			_ = json.Unmarshal(events[0], &event)
			if event.Type == responseevents.Created {
				return append(append(events[:1:1], notices...), events[1:]...), nil
			}
		}
		return append(notices, events...), nil
	}
	var provider struct {
		Response json.RawMessage `json:"response"`
	}
	if err := json.Unmarshal(original, &provider); err != nil {
		return nil, err
	}
	terminalMessages, err := t.journalTerminalMessages(provider.Response)
	if err != nil {
		t.ReleaseDelivery()
		return nil, err
	}
	// Stream the same ordering as the terminal snapshot, leaving the final-answer
	// journal last with no commentary after it.
	if t.subagentTurn {
		messages = append(messages, terminalMessages...)
	} else {
		messages = append(terminalMessages, messages...)
	}
	var notices [][]byte
	for _, message := range messages {
		notices = append(notices, assistantCommentaryDoneEvent(message))
	}
	var visible [][]byte
	for _, payload := range events {
		var event struct {
			Type     responseevents.Kind        `json:"type"`
			Item     map[string]json.RawMessage `json:"item"`
			Response map[string]json.RawMessage `json:"response"`
		}
		if err := json.Unmarshal(payload, &event); err != nil {
			return nil, err
		}
		if event.Type == responseevents.Completed {
			var output []map[string]json.RawMessage
			if err := decodeJournalOutput(event.Response["output"], &output); err != nil {
				return nil, err
			}
			output = t.withoutNaturalAnswer(output)
			output = append(output, messages...)
			event.Response["output"] = mustMarshalJSON(output)
			payload, err = replaceRawField(payload, "response", mustMarshalJSON(event.Response))
			if err != nil {
				return nil, err
			}
			visible = append(visible, notices...)
		}
		visible = append(visible, payload)
	}
	return visible, nil
}

func decodeJournalOutput(raw json.RawMessage, output *[]map[string]json.RawMessage) error {
	if len(raw) == 0 {
		*output = nil
		return nil
	}
	return json.Unmarshal(raw, output)
}

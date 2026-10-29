package executionobservation

import (
	"bytes"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"strings"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
	"github.com/yusing/mekugi/modules/agentguidance/compact"
)

// Prepared children reuse the compaction reader's native scheduling, continuation
// and retained snapshot projection. The admitted assignment, not message text,
// selects the frozen preload; once compaction drops it, it cannot trigger rereads.
func (p *Preparer) preparePreloadInput(request *codexresponses.Request, session, thread string, metadata codexresponses.TurnMetadata, prewarm bool) (jsontext.Value, map[string]bool, []activity.Supplement, error) {
	if !bytes.Contains(request.Fields["input"], []byte("threadcontrol_")) {
		return nil, nil, nil, nil
	}
	if metadata.ActivityIdentityInvalid || metadata.ThreadID != "" && metadata.ThreadID != thread {
		return nil, nil, nil, errors.New("context preload requires matching runtime identity")
	}
	selections, err := p.threadControl.Coordination().ContextPreloads(thread, session)
	if err != nil || len(selections) == 0 {
		return nil, nil, nil, err
	}
	var items []map[string]jsontext.Value
	if err := json.Unmarshal(request.Fields["input"], &items); err != nil {
		return nil, nil, nil, err
	}
	assignments := make(map[string]bool)
	for _, item := range items {
		if responses.StringField(item, "type") == "message" && responses.StringField(item, "role") == "assistant" {
			assignments[responses.StringField(item, "id")] = true
		}
	}
	var pending jsontext.Value
	var supplements []activity.Supplement
	calls := make(map[string]bool)
	for _, selection := range selections {
		if !assignments[selection.Item] {
			continue
		}
		record := compact.Record{Reference: selection.Item, Selection: selection.Selection}
		call, readCalls, err := compactReaderCall(record, items, thread)
		for _, id := range readCalls {
			calls[id] = true
		}
		if prewarm || metadata.RequestKind != responses.Turn || codexresponses.ExecutionFree(request) {
			continue
		}
		if err != nil {
			return nil, nil, nil, err
		}
		if len(call) > 0 {
			pending = call
			sources, _ := compact.ReloadSources(selection.Selection)
			command := compact.CaptureCommand(selection.Selection)
			var reloads []activity.ReloadSource
			for _, source := range sources {
				reloads = append(reloads, activity.ReloadSource{Command: command, Path: source.Path, Skill: source.Skill, Changes: strings.Join(source.Changes, " "), Preload: true})
			}
			supplements = append(supplements, activity.Supplement{Thread: thread, Turn: metadata.TurnID, Source: selection.Item, Kind: "compact_reload", Text: "Context preload", ReloadSources: reloads})
		}
	}
	return pending, calls, supplements, nil
}

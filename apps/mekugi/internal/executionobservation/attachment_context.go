package executionobservation

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"

	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	"github.com/yusing/mekugi/modules/agentguidance/compact"
)

func (p *Preparer) prepareAttachmentContext(ctx context.Context, request *codexresponses.Request, thread string, metadata codexresponses.TurnMetadata, prewarm bool) (jsontext.Value, map[string]bool, error) {
	if p.ContextAttachments == nil || prewarm || metadata.RequestKind != responses.Turn || metadata.WindowNumber == 0 || metadata.ContextWindowID == "" {
		return nil, nil, nil
	}
	values, err := p.ContextAttachments(ctx, thread, metadata.ContextWindowID)
	if err != nil {
		return nil, nil, err
	}
	if len(values) == 0 {
		return nil, nil, nil
	}
	if err := requestexecution.RestoreAttachments(request, values); err != nil {
		return nil, nil, err
	}
	var items []map[string]jsontext.Value
	if err := json.Unmarshal(request.Fields["input"], &items); err != nil {
		return nil, nil, err
	}
	// A caller-selected whole skill already uses the shared current-source reader.
	// Keep its result and failure instead of scheduling an identical second read.
	selections, _, err := preloadSelections(items)
	if err != nil {
		return nil, nil, err
	}
	selected := make(map[string]bool)
	for _, preload := range selections {
		sources, err := compact.ReloadSources(preload.Selection)
		if err != nil {
			return nil, nil, err
		}
		for _, source := range sources {
			if len(source.Ranges) == 0 && source.Changes == nil {
				selected[source.Skill+"\x00"+source.Path] = true
			}
		}
	}
	var sources []compact.Source
	for _, value := range values {
		for _, skill := range value.Skills {
			key := skill.Name + "\x00" + skill.Path
			if selected[key] {
				continue
			}
			selected[key] = true
			sources = append(sources, compact.Source{Skill: skill.Name, Path: skill.Path})
		}
	}
	if len(sources) == 0 {
		return nil, nil, nil
	}
	selection := responses.MustMarshalJSON(struct {
		Sources []compact.Source `json:"sources"`
	}{sources})
	call, readers, err := compactReaderCall(compact.Record{Reference: "attachments:" + metadata.ContextWindowID, Selection: string(selection)}, items, thread)
	calls := make(map[string]bool, len(readers))
	for _, id := range readers {
		calls[id] = true
	}
	if codexresponses.ExecutionFree(request) {
		return nil, calls, nil
	}
	return call, calls, err
}

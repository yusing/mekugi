package executionobservation

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"strings"

	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	"github.com/yusing/mekugi/modules/agentguidance/compact"
	"github.com/yusing/mekugi/modules/conversation/composer"
)

type compactionCost struct {
	reader  jsontext.Value
	calls   map[string]bool
	reload  []jsontext.Value
	known   bool
	unknown string
}

// Preflight uses the same native reader as post-reset reload, in the same
// admitted caller. Files are assumed unchanged across the reset, not frozen.
// Scheduling happens only after observeThreadWait confirms the enclosing cell.
func (p *Preparer) prepareCompactionCost(ctx context.Context, request *codexresponses.Request, thread string, metadata codexresponses.TurnMetadata) *compactionCost {
	if metadata.RequestKind != "turn" || metadata.ActivityIdentityInvalid || metadata.ThreadID != "" && metadata.ThreadID != thread {
		return nil
	}
	wait, err := p.ThreadControl.Coordination().WaitForTurn(thread, metadata.TurnID)
	if err != nil || wait == nil || wait.State != "registered" || wait.Stop != "eligible" || wait.Compaction == nil || strings.HasPrefix(request.Model(), "claude:") {
		return nil
	}
	// Cost vetoes apply only to measured context usage at or below 40%.
	// Skip preflight acquisition entirely when the gate cannot veto a reset.
	if p.Inspect == nil {
		return nil
	}
	details, err := p.Inspect(ctx, thread)
	if err != nil || details.ContextTokens == nil || details.ContextWindow == nil || *details.ContextWindow == 0 || float64(*details.ContextTokens) > .4*float64(*details.ContextWindow) {
		return nil
	}
	_, _, priced := p.Usage.CompareInputCost(thread, request.Model(), codexresponses.UsageServiceTier(request.Fields["service_tier"]), 0)
	if !priced {
		return &compactionCost{unknown: "current model/tier input pricing is unavailable"}
	}
	check := &compactionCost{calls: make(map[string]bool)}
	sources, err := compact.ReloadSources(wait.Compaction.Message)
	if err != nil {
		return check
	}
	if p.CompactionAttachments != nil {
		attached, err := p.CompactionAttachments(ctx, thread)
		if err != nil {
			return check
		}
		kinds := make(map[string]bool)
		for _, value := range attached {
			kinds["selections"] = kinds["selections"] || len(value.Selection.Frames) > 0
			kinds["images"] = kinds["images"] || len(value.Images) > 0
			for _, label := range value.Placeholders {
				if strings.HasPrefix(label, "$") {
					kinds["skills"] = true
				} else if !composer.SelectionLabel(label) {
					kinds["files"] = true
				}
			}
			for _, skill := range value.Skills {
				found := false
				for _, source := range sources {
					found = found || source.Skill == skill.Name && source.Path == skill.Path && len(source.Ranges) == 0 && source.Changes == nil
				}
				if !found {
					kinds["skills"] = true
				}
			}
		}
		// Automatic attachments expose recall handles, not their retained bodies.
		// Future opaque handles use a one-token placeholder in this estimate.
		var hints []string
		for _, kind := range []string{"selections", "skills", "files", "images"} {
			if kinds[kind] {
				hints = append(hints, kind+": mread reference")
			}
		}
		if len(hints) > 0 {
			check.reload = append(check.reload, responses.MustMarshalJSON(map[string]any{"type": "message", "role": "user", "content": []map[string]string{{"type": "input_text", "text": attachmentRecallHint(hints)}}}))
		}
	}
	selection, err := json.Marshal(struct {
		Sources []compact.SourceIdentity `json:"sources"`
	}{sources})
	if err != nil {
		return check
	}
	var items []map[string]jsontext.Value
	if json.Unmarshal(request.Fields["input"], &items) != nil {
		return check
	}
	reader, calls, err := compactReaderCall(compact.Record{Reference: "cost:" + wait.ID, Selection: string(selection)}, items, thread)
	for _, id := range calls {
		check.calls[id] = true
	}
	if err != nil || wait.Compaction.State != "registered" {
		return check
	}
	check.reader = reader
	if len(reader) > 0 {
		return check
	}
	if len(calls) == 0 {
		check.known = len(sources) == 0
		return check
	}
	// Preflight reads measure a possible reload. They do not attach context or
	// publish attachment outcomes before the reset has actually succeeded.
	outputs, err := p.compactSnapshots(ctx, request.Fields["input"], check.calls, thread, metadata, false, &attachmentPreparation{})
	if err != nil {
		return check
	}
	check.known = len(sources) == 0
	var captured []map[string]jsontext.Value
	for _, id := range calls {
		if parts := outputs[id]; len(parts) > 2 {
			captured = append(captured, map[string]jsontext.Value{"type": responses.MustMarshalJSON("custom_tool_call_output"), "call_id": responses.MustMarshalJSON(id), "output": parts})
			check.known = true
		}
	}
	projected, err := compactReaderInput(captured, check.calls)
	var reload []jsontext.Value
	if err != nil || json.Unmarshal(projected, &reload) != nil {
		check.known = false
		return check
	}
	check.reload = append(check.reload, reload...)
	return check
}

func (t *mekugiResponseTransform) checkCompactionCost(request *codexresponses.Request) error {
	check := t.compactionCost
	if check == nil || t.localWaitItem == "" {
		return nil
	}
	if len(check.reader) > 0 || len(t.compactReload) > 0 {
		if len(t.compactReload) == 0 {
			t.compactReload = check.reader
		}
		t.localWaitItem = "" // Native preflight must finish before the local stop.
		return nil
	}
	if !check.known {
		if len(check.calls) > 0 {
			return t.owners.ThreadControl.Coordination().RejectCompaction(t.ctx, t.shellThreadID, t.shellTurnID, t.localWaitItem, "Preflight did not produce usable evidence; failed or canceled reads were not repeated. Context was not reset; reload sources were not attached.")
		}
		reason := check.unknown
		if reason == "" {
			reason = "reload acquisition did not supply complete evidence"
		}
		t.owners.notice(t.sessionID, t.shellThreadID, "compaction_cost", "Self-compaction cost estimate unavailable: "+reason+"; proceeding with reset.")
		return nil
	}
	tokens, err := requestexecution.EstimateResetInput(request, check.reload)
	if err != nil {
		t.owners.notice(t.sessionID, t.shellThreadID, "compaction_cost", "Self-compaction cost estimate unavailable: prepared input could not be measured; proceeding with reset.")
		return nil
	}
	current, cold, known := t.owners.Usage.CompareInputCost(t.shellThreadID, request.Model(), codexresponses.UsageServiceTier(request.Fields["service_tier"]), tokens)
	if !known {
		t.owners.notice(t.sessionID, t.shellThreadID, "compaction_cost", "Self-compaction cost estimate unavailable: input pricing is unavailable at the estimated size; proceeding with reset.")
		return nil
	}
	if cold.Total() <= current.Total() {
		return nil
	}
	reason := fmt.Sprintf("Estimated uncached next input $%.6f exceeds last observed input $%.6f, including its cache discount (%d reset tokens; assumes no cache hits after reset and unchanged reload sources). Context was not reset; reload sources were not attached.", cold.Total(), current.Total(), tokens)
	return t.owners.ThreadControl.Coordination().RejectCompaction(t.ctx, t.shellThreadID, t.shellTurnID, t.localWaitItem, reason)
}

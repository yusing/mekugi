package router

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/yusing/mekugi/internal/responses"
)

// Pinned workflow conflicts, not a classifier for arbitrary caller policy.
// Whole physical spans protect caller qualifications and adjacent safety rules.
var orchestrateInstructionConflicts = [][2]string{
	{"- `MAIN.md`: main: planning, delegation, and coordination.", ""},
	{"- `REVIEW.md`: main: after an authorized change is implemented and focused-validated,\n  before completion or a slice commit; read it then even if read earlier.", ""},
	{"Use native role capabilities within authorized scope. Main owns integration/completion and edits\ninstructions, skills, workflow/task guidance, and delivery hooks; delegates return findings/proposals.", "Use native role capabilities within authorized scope. Main owns integration/completion."},
	{"Main owns scope, decisions, implementation, integration, and completion within existing authorization.", "Main owns scope, decisions, integration, and completion within existing authorization."},
	{"Main owns applicability, behavior, owner discovery, integration, and the timing/authorship policy\nin MAIN.md. Instructions, skills, workflow/task documents, and model-facing templates stay main's.", ""},
	{"Update affected specs, contracts, and reader documents as decisions settle during exploration.\nDescribe the intended behavior and distinguish planned work from verified results; do not invent\nvalidation or performance evidence. DOCS.md owns document consistency. Main keeps document work by\ndefault; delegate a bounded document assignment only when it replaces independent work at lower\ncost. Instructions, skills, workflow/task documents, and model-facing templates stay with main.", "Update affected specs, contracts, and reader documents as decisions settle during exploration.\nDescribe the intended behavior and distinguish planned work from verified results; do not invent\nvalidation or performance evidence. DOCS.md owns document consistency."},
	{"Delegate independent factual lookup and bounded support work when it replaces main's work at lower\ncost. Keep next-step-dependent discovery and production changes local. Instruction audits and edits\nat named owners remain main's. Select roles from the native catalog; do not disguise diagnosis as lookup.", "Select roles from the native catalog; do not disguise diagnosis as lookup."},
	{"Main keeps tightly coupled regressions, broken-test migration, and snapshots. Delegate coherent\nacceptance, differential, fixture-heavy, or cross-cutting suites when the independent outcome warrants\nhandoff. Before dispatch, reuse a passing focused build/check that establishes the assigned seam;\nrequired definitions must already exist and the interface must be stable. A check from before later\nedits does not establish it, and a seam main is still changing is not stable. Otherwise finish that\nimplementation first. Workers report production/dependency fixes to main.", ""},
}

func maskOrchestrateInstructions(source string) string {
	return rewriteUnfencedInstructionSpans(source, func(text string) string {
		for _, pinned := range orchestrateInstructionConflicts {
			for _, ending := range []string{"\n", "\r\n"} {
				pair := [2]string{strings.ReplaceAll(pinned[0], "\n", ending), strings.ReplaceAll(pinned[1], "\n", ending)}
				for offset := 0; offset < len(text); {
					index := strings.Index(text[offset:], pair[0])
					if index < 0 {
						break
					}
					start := offset + index
					end := start + len(pair[0])
					if (start == 0 || text[start-1] == '\n') && (end == len(text) || text[end] == '\n' || strings.HasPrefix(text[end:], "\r\n")) {
						text = text[:start] + pair[1] + text[end:]
						offset = start + len(pair[1])
					} else {
						offset = end
					}
				}
			}
		}
		return text
	})
}

// Read the same manifest-backed ancestry used for cross-checkout journals.
// Request text and live UI state never grant orchestration membership.
func (p *mekugiProxy) projectOrchestrateInstructions(ctx context.Context, request *parsedResponsesRequest, thread string, metadata codexTurnMetadata, valid bool) error {
	if !valid || thread == "" || metadata.activityIdentityInvalid || metadata.ThreadID != "" && metadata.ThreadID != thread || p.journals == nil {
		return nil
	}
	workspace, selected := usableRoutingDirectory(metadata.Directories)
	if !selected && len(metadata.Directories) == 0 && metadata.RequestKind == responses.Compaction && p.replayStore != nil {
		store := p.replayStore.scoped(ctx)
		err := store.locked(ctx, func() error {
			var err error
			workspace, err = store.compactionWorkspace(thread)
			return err
		})
		if err != nil && !errors.Is(err, errNoRetainedJournalWorkspace) {
			return err
		}
		selected = err == nil
	}
	if !selected {
		return nil
	}
	guidance := ""
	err := p.journals.transactionWithTiming(ctx, p.replayStore, workspace, thread, false, func(j *threadJournal, exists bool) error {
		if exists && j.Parent == "" && j.Orchestration == nil || !exists && metadata.SubagentKind == "" {
			return errJournalUnchanged
		}
		// A fresh native descendant has not yet saved its own identity. Its host
		// parent metadata still selects a retained ancestry, never a prompt claim.
		caller := thread
		if !exists && metadata.SubagentKind != "" {
			caller = metadata.ParentThreadID
		}
		journals, failures, err := p.journals.relatedJournals(p.replayStore.scoped(ctx), workspace, caller)
		if err != nil {
			return err
		}
		chain := journalAncestry(journals, caller)
		if len(chain) == 0 {
			return errJournalUnchanged
		}
		for _, id := range chain {
			if failures[id] != nil {
				return failures[id]
			}
		}
		root := journals[chain[len(chain)-1]]
		if root.Orchestration == nil {
			return errJournalUnchanged
		}
		guidance = orchestrateOwnershipInstructions
		if thread == root.Orchestration.Main {
			guidance = "This thread is Main.\n" + guidance
		} else if exists && j.Parent == "" {
			guidance = "This thread is an orchestration child.\n" + guidance
		}
		return errJournalUnchanged
	})
	if err != nil {
		return fmt.Errorf("resolve orchestration instructions: %w", err)
	}
	if guidance == "" {
		return nil
	}
	if original, ok := decodeJSONString(request.fields["instructions"]); ok {
		request.fields["instructions"] = mustMarshalJSON(maskOrchestrateInstructions(original))
	}
	if err := rewriteRequestMessages(request, "orchestration instructions", func(item responsesItem) ([]byte, bool, error) {
		transform := maskOrchestrateInstructions
		switch item.Role {
		case "system", "developer":
		case "user":
			transform = func(text string) string { return transformUserInstructions(text, maskOrchestrateInstructions) }
		default:
			return nil, false, nil
		}
		content, found, err := transformResponsesTextContent(item.Content, transform, isResponsesInputTextPart)
		return content, found && string(content) != string(item.Content), err
	}); err != nil {
		return err
	}
	const start, end = "<!-- mekugi-orchestration:start -->", "<!-- mekugi-orchestration:end -->"
	original, _ := decodeJSONString(request.fields["instructions"])
	projected, err := refreshMarkedToolGuidance(original, start, end, start+"\n"+guidance+"\n"+end)
	if err != nil {
		return err
	}
	request.fields["instructions"] = mustMarshalJSON(projected)
	return nil
}

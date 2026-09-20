package router

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/yusing/mekugi"
	"github.com/yusing/mekugi/internal/pathdisplay"
	"mvdan.cc/sh/v3/interp"
)

// executeHpatch consumes shell-expanded argv and stdin, never shell source.
func executeHpatch(ctx context.Context, manifest toolWorkerManifest, arguments []string, commentary shellCommentarySink) error {
	handler := interp.HandlerCtx(ctx)
	fail := func(err error) error {
		_, _ = fmt.Fprintf(handler.Stderr, "hpatch: %v\n", err)
		return interp.ExitStatus(1)
	}

	var recoveryID string
	if len(arguments) >= 2 && arguments[0] == "--recover" {
		recoveryID = arguments[1]
		arguments = arguments[2:]
	}

	var (
		edits   []mekugi.FileEdit
		emitted string
	)
	thread := handler.Env.Get("CODEX_THREAD_ID").String()
	var recoveryBase mekugiHistory
	var scriptIndex int
	if recoveryID != "" {
		payload, parsedScriptIndex, err := parseHpatchRecoveryArguments(&handler, arguments)
		if err != nil {
			return fail(err)
		}
		scriptIndex = parsedScriptIndex
		store, err := shellOutputStore(manifest)
		if err != nil {
			return fail(err)
		}
		base, err := store.rejectedEdit(ctx, handler.Dir, recoveryID)
		if err != nil {
			return fail(err)
		}
		recovered, err := recoverBatchDetailed(ctx, base.Edits, payload, base.RecoveryHandles, scriptIndex)
		if err != nil {
			return fail(err)
		}
		edits = recovered.edits
		emitted = payload
		recoveryBase = base
	} else {
		var err error
		edits, err = parseHpatchEdits(&handler, arguments)
		if err != nil {
			return fail(err)
		}
		if len(edits) == 1 {
			emitted = edits[0].Script
		} else {
			emitted = hpatchHistoryScript(edits)
		}
	}

	store, err := shellOutputStore(manifest)
	if err != nil {
		return fail(err)
	}
	handles, err := store.allocateHandles(ctx, 1)
	if err != nil {
		return fail(err)
	}
	recoveryHandle := handles[0]
	callID := "hpatch-" + recoveryHandle

	correlationID := callID
	attempt := 1
	if recoveryID != "" {
		correlationID = recoveryBase.CorrelationID
		attempt = recoveryBase.Attempt + 1
	}
	changeID, err := store.reserveChange(ctx, handler.Dir, thread, correlationID)
	if err != nil {
		return fail(err)
	}
	evaluated := hpatchHistoryScript(edits)
	attemptContext := mekugi.WithAttemptMetadata(ctx, mekugi.AttemptMetadata{
		SessionID: thread, CallID: callID, CorrelationID: correlationID, Attempt: attempt,
		Correction: recoveryID != "", ToolName: mekugiToolName,
		EmittedPayload: emitted, EvaluatedScript: evaluated,
	})
	result, applyErr := mekugi.ApplyForHostAt(attemptContext, handler.Dir, edits, manifest.HookDirectory)

	history := mekugiHistory{
		ToolName: mekugiToolName, Script: emitted, Edits: cloneHpatchEdits(edits),
		Evaluated: retainedEvaluated(emitted, evaluated), RecoveryScript: scriptIndex, Root: handler.Dir, ExecutingThread: thread,
		ChangeID: changeID, CorrelationID: correlationID, Attempt: attempt,
		Report:      changeNotice(changeID) + mekugiReport(result.Report, result.Diagnostic),
		ReviewFiles: result.ReviewFiles, Applied: result.Change.Applied,
		AlreadySatisfied: result.Change.AlreadySatisfied,
	}
	if applyErr != nil {
		history.EvaluatorRejected = len(result.Rejections) != 0
		history.Rejections = result.Rejections
		if history.EvaluatorRejected {
			count := len(recoveryBatchCommands(edits, nil))
			history.RecoveryHandles, err = store.allocateHandles(ctx, count)
			if err != nil {
				return fail(err)
			}
			history.RecoveryBinding = recoveryBatchHandlesBinding(edits, history.RecoveryHandles)
		}
		history.TranslationError = result.Diagnostic
		if history.TranslationError == "" {
			history.TranslationError = applyErr.Error()
		}
	}
	if history.EvaluatorRejected {
		if len(edits) == 1 {
			history.TranslationError += mekugiRecoveryGuidance(edits[0].Script, result.Rejections, recoveryID != "", history.RecoveryHandles)
		} else {
			history.TranslationError += mekugiRecoveryGuidanceBatch(edits, result.Rejections, recoveryID != "", history.RecoveryHandles)
		}
		history.TranslationError += "\nRecover this rejected edit with hpatch --recover " + recoveryHandle + ".\n"
	}
	if err := store.put(context.WithoutCancel(ctx), handler.Dir, map[string]mekugiHistory{callID: history}); err != nil {
		return fail(fmt.Errorf("edit applied=%t; retaining change evidence: %w", result.Change.Applied, err))
	}
	if publisher, ok := commentary.(interface {
		PublishEdit(context.Context, string, string) error
	}); ok {
		_ = publisher.PublishEdit(ctx, handler.Dir, callID)
	}
	if applyErr != nil {
		return fail(fmt.Errorf("%s%s", changeNotice(changeID), history.TranslationError))
	}
	_, err = io.WriteString(handler.Stdout, history.Report)
	return err
}

func hpatchHistoryScript(edits []mekugi.FileEdit) string {
	if len(edits) == 0 {
		return ""
	}
	if len(edits) == 1 {
		return edits[0].Script
	}
	var batch strings.Builder
	for _, edit := range edits {
		fmt.Fprintf(&batch, "file %q:\n%s", edit.Path, edit.Script)
		if !strings.HasSuffix(edit.Script, "\n") {
			batch.WriteByte('\n')
		}
	}
	return batch.String()
}

func cloneHpatchEdits(edits []mekugi.FileEdit) []mekugi.FileEdit {
	if len(edits) == 0 {
		return nil
	}
	return append([]mekugi.FileEdit(nil), edits...)
}

func (s *mekugiReplayStore) rejectedEdit(ctx context.Context, workspace, id string) (mekugiHistory, error) {
	s = s.scoped(ctx)
	var history mekugiHistory
	err := s.locked(ctx, func() error {
		record, found, err := s.read(workspace, "hpatch-"+id, false)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("change %s has no retained edit", id)
		}
		history = record.History
		binding := recoveryHandlesBinding(history.recoveryBaseline(), history.RecoveryHandles)
		if len(history.Edits) != 0 {
			binding = recoveryBatchHandlesBinding(history.Edits, history.RecoveryHandles)
		}
		if history.TranslationError == "" || !history.EvaluatorRejected ||
			history.RecoveryBinding != binding {
			return fmt.Errorf("recovery %s has no recoverable rejected edit", id)
		}
		return nil
	})
	return history, err
}

// publishEditReceipt reads committed evidence, never caller-supplied diffs.
// The runtime capability must belong to the executing attempt, which may be a
// fork continuing a change originated by another thread.
func (s *mekugiReplayStore) publishEditReceipt(ctx context.Context, workspace, thread, callID string, activity *subagentActivity) error {
	s = s.scoped(ctx)
	return s.locked(ctx, func() error {
		record, found, err := s.read(workspace, callID, false)
		if err != nil {
			return err
		}
		if !found || (record.History.ToolName != mekugiToolName && record.History.ToolName != "shell") || record.History.ExecutingThread != thread {
			return fmt.Errorf("edit receipt not found")
		}
		index, err := s.readChangeIndex(workspace)
		if err != nil {
			return err
		}
		id := record.History.ChangeID
		for _, call := range index.Changes[id].Calls {
			if call.ID == callID && call.Thread == thread {
				if record.History.Applied && record.History.TranslationError == "" {
					var summaries []string
					for _, file := range record.History.ReviewFiles {
						path := file.AfterPath
						if path == "" {
							path = file.BeforePath
						}
						path = pathdisplay.ForWorkspace(workspace, path)
						if file.Incomplete != "" {
							summaries = append(summaries, fmt.Sprintf("Edit %s: incomplete history; line counts unavailable", commentaryCode(path)))
							continue
						}
						added, removed := file.LineCounts()
						summaries = append(summaries, fmt.Sprintf("Edit %s +%d -%d", commentaryCode(path), added, removed))
					}
					activity.collect(thread, "edit-receipt\x00"+workspace+"\x00"+callID, "tool", strings.Join(summaries, "\n\n"))
				}
				s.notifyLiveDiff(index, map[string][]trackedCall{id: {call}})
				return nil
			}
		}
		return fmt.Errorf("edit receipt does not belong to publishing thread")
	})
}

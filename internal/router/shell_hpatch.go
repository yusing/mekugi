package router

import (
	"context"
	"fmt"
	"io"

	"github.com/yusing/mekugi"
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
	if len(arguments) > 1 {
		return fail(fmt.Errorf("usage: hpatch [--recover HANDLE] [SCRIPT]; omit SCRIPT to read stdin"))
	}
	var source string
	if len(arguments) == 1 {
		source = arguments[0]
	} else if handler.Stdin != nil {
		data, err := io.ReadAll(io.LimitReader(handler.Stdin, maxMekugiScriptBytes+1))
		if err != nil {
			return fail(err)
		}
		source = string(data)
	}
	if len(source) > maxMekugiScriptBytes {
		return fail(fmt.Errorf("script exceeds %d bytes", maxMekugiScriptBytes))
	}
	store, err := shellOutputStore(manifest)
	if err != nil {
		return fail(err)
	}
	thread := handler.Env.Get("CODEX_THREAD_ID").String()
	handles, err := store.allocateHandles(ctx, 1)
	if err != nil {
		return fail(err)
	}
	recoveryHandle := handles[0]
	callID := "hpatch-" + recoveryHandle
	emitted := source

	correlationID := callID
	attempt := 1
	if recoveryID != "" {
		base, err := store.rejectedEdit(ctx, handler.Dir, recoveryID)
		if err != nil {
			return fail(err)
		}
		recovered, err := recoverScriptDetailed(ctx, base.recoveryBaseline(), source, base.RecoveryHandles)
		if err != nil {
			return fail(err)
		}
		source = recovered.script
		correlationID = base.CorrelationID
		attempt = base.Attempt + 1
	}
	changeID, err := store.reserveChange(ctx, handler.Dir, thread, correlationID)

	if err != nil {
		return fail(err)
	}
	attemptContext := mekugi.WithAttemptMetadata(ctx, mekugi.AttemptMetadata{
		SessionID: thread, CallID: callID, CorrelationID: correlationID, Attempt: attempt,
		Correction: recoveryID != "", ToolName: mekugiToolName,
		EmittedPayload: emitted, EvaluatedScript: source,
	})
	result, applyErr := mekugi.ApplyForHostAt(attemptContext, handler.Dir, source, manifest.HookDirectory)

	history := mekugiHistory{
		ToolName: mekugiToolName, Script: emitted, Evaluated: retainedEvaluated(emitted, source), Root: handler.Dir, ExecutingThread: thread,
		ChangeID: changeID, CorrelationID: correlationID, Attempt: attempt,
		Report:      changeNotice(changeID) + mekugiReport(result.Report, result.Diagnostic),
		ReviewFiles: result.ReviewFiles, Applied: result.Change.Applied,
		AlreadySatisfied: result.Change.AlreadySatisfied,
	}
	if applyErr != nil {
		history.EvaluatorRejected = len(result.Rejections) != 0
		history.Rejections = result.Rejections
		if history.EvaluatorRejected {
			history.RecoveryHandles, err = store.allocateHandles(ctx, len(recoveryCommands(source, nil)))
			if err != nil {
				return fail(err)
			}
			history.RecoveryBinding = recoveryHandlesBinding(source, history.RecoveryHandles)
		}
		history.TranslationError = result.Diagnostic
		if history.TranslationError == "" {
			history.TranslationError = applyErr.Error()
		}
	}
	if history.EvaluatorRejected {
		history.TranslationError += mekugiRecoveryGuidance(source, result.Rejections, recoveryID != "", history.RecoveryHandles)
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
		if history.TranslationError == "" || !history.EvaluatorRejected ||
			history.RecoveryBinding != recoveryHandlesBinding(history.recoveryBaseline(), history.RecoveryHandles) {
			return fmt.Errorf("recovery %s has no recoverable rejected edit", id)
		}
		return nil
	})
	return history, err
}

// publishEditReceipt reads committed evidence, never caller-supplied diffs.
// The runtime capability must belong to the executing attempt, which may be a
// fork continuing a change originated by another thread.
func (s *mekugiReplayStore) publishEditReceipt(ctx context.Context, workspace, thread, callID string) error {
	s = s.scoped(ctx)
	return s.locked(ctx, func() error {
		record, found, err := s.read(workspace, callID, false)
		if err != nil {
			return err
		}
		if !found || record.History.ToolName != mekugiToolName || record.History.ExecutingThread != thread {
			return fmt.Errorf("edit receipt not found")
		}
		index, err := s.readChangeIndex(workspace)
		if err != nil {
			return err
		}
		id := record.History.ChangeID
		for _, call := range index.Changes[id].Calls {
			if call.ID == callID && call.Thread == thread {
				s.notifyLiveDiff(index, map[string][]trackedCall{id: {call}})
				return nil
			}
		}
		return fmt.Errorf("edit receipt does not belong to publishing thread")
	})
}

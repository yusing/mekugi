package router

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/contracts/scope"
	"github.com/yusing/mekugi/modules/outputrecovery/recovery"
)

func retainedScope(ctx context.Context, directory string) scope.Retained {
	identity, _ := ctx.Value(storageSessionKey{}).(storageSessionIdentity)
	return scope.Retained{Directory: directory, Namespace: identity.Namespace, Thread: identity.Thread}
}
func (s *mekugiReplayStore) recoverySession(ctx context.Context) (*recovery.Session, error) {
	s = s.scoped(ctx)
	return s.recovery.Attach(scope.Retained{Directory: s.directory, Namespace: s.session.Namespace, Thread: s.session.Thread}, s.retentionLimits(), s.writes)
}
func workerReplayStore(manifest toolWorkerManifest) (*mekugiReplayStore, error) {
	if manifest.ReplayDirectory == "" {
		return nil, errors.New("read recovery storage is unavailable")
	}
	info, err := os.Lstat(filepath.Join(manifest.ReplayDirectory, "store.lock"))
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("read recovery storage is missing or invalid")
	}
	return &mekugiReplayStore{publication: manifest.history.Attach(manifest.ReplayDirectory), recovery: manifest.recovery, directory: manifest.ReplayDirectory, maxBytes: defaultReplayStorageBytes, maxCommentaryBytes: 16 << 20, storageNotice: func(_, _, _ string, message string) { _, _ = fmt.Fprintln(os.Stderr, message) }}, nil
}

func retainExecutionOutput(ctx context.Context, manifest toolWorkerManifest, execution command.Output) (command.Output, error) {
	if execution.OmittedOutput == nil && execution.Delivery == nil {
		return execution, nil
	}
	store, err := manifest.recovery.Attach(retainedScope(ctx, manifest.ReplayDirectory), recovery.DefaultLimits(), nil)
	if err != nil {
		return execution, err
	}
	if execution.Delivery != nil {
		plan := execution.Delivery
		execution.Delivery = nil
		handles, err := store.Reserve(ctx, plan.ReferenceCount())
		if err != nil {
			return execution, err
		}
		prepared, err := plan.Prepare(handles)
		if err != nil {
			return execution, err
		}
		for _, output := range prepared.Recovery {
			if output.ReferenceIndex < 0 || output.ReferenceIndex >= len(handles) {
				return prepared.Unreferenced, errors.New("recovery reference outside reserved slots")
			}
			id := handles[output.ReferenceIndex]
			if len(output.ChangeSelection) != 0 {
				if output.OmittedOutput != (command.OmittedOutput{}) || output.ExitCode != 0 {
					return prepared.Unreferenced, errors.New("change selection cannot contain omitted output")
				}
				_, err = store.PublishSelectionAt(ctx, id, output.ChangeSelection)
			} else {
				_, err = store.PublishAt(ctx, id, output.OmittedOutput, output.ExitCode)
			}
			if err != nil {
				return prepared.Unreferenced, storagePublicationError(err)
			}
		}
		return prepared.Referenced, nil
	}
	id, err := store.Publish(ctx, *execution.OmittedOutput, execution.ExitCode)
	if err != nil {
		return execution, err
	}
	omitted := execution.OmittedOutput
	execution.OmittedOutput = nil
	if omitted.SourceRow != 0 {
		execution.Stderr = strings.TrimSuffix(execution.Stderr, "\n") + "; " + strings.TrimPrefix(recovery.NextCall(id, omitted.MaxTokens), "read: incomplete; ")
	} else {
		execution.Stderr += recovery.NextCall(id)
	}
	return execution, nil
}

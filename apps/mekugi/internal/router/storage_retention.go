package router

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/recordtransactions"
	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"
	"github.com/yusing/mekugi/libraries/legacycall"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	"github.com/yusing/mekugi/libraries/persistence"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	"github.com/yusing/mekugi/modules/diagnostics/diagnostic"
	"github.com/yusing/mekugi/modules/outputrecovery/recovery"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

type storageSessionKey struct{}

type storageSessionIdentity struct {
	Thread     string
	Routing    string
	Namespace  string
	Parent     string
	Fork       string
	Initialize bool
}

func observationScope(ctx context.Context, selected changes.Scope) changes.Scope {
	if identity, ok := ctx.Value(storageSessionKey{}).(storageSessionIdentity); ok {
		selected.Thread, selected.Namespace = identity.Thread, identity.Namespace
	}
	return selected
}

// Application scope carries routing authority without giving it to the store.
func beginRetainedSession(ctx context.Context, store *history.Store, writes *persistence.Counter, limits history.Limits, thread, routing string) (context.Context, func(), error) {
	if store == nil || thread == "" {
		return ctx, func() {}, nil
	}
	namespace, release, err := store.BeginSession(ctx, writes, limits, thread)
	if publication, ok := errors.AsType[*history.PublicationError](err); ok {
		err = requestexecution.StoragePublicationError(publication.Err)
	}
	if err != nil {
		return ctx, nil, err
	}
	identity := storageSessionIdentity{Thread: thread, Routing: routing, Namespace: namespace}
	return context.WithValue(ctx, storageSessionKey{}, identity), release, nil
}

var retainedReadReference = regexp.MustCompile(`(?:\b|\\[nr])mread(?:[ \t]|\\t)+([a-z]+[0-9]*\b(?:(?:[ \t]|\\t)+[a-z]+[0-9]*\b)*)`)

// Retain the complete visible history in one locked catalog update. A fork
// shares immutable records with its source; removing either chat keeps facts
// referenced by the other. Invalid input never establishes an ownership claim.
func retainObservationInput(ctx context.Context, selected changes.Scope, source changes.Service, recovered *recovery.Service, workspace string, raw json.RawMessage, histories map[string]mekugiHistory, releaseSnapshot func()) error {
	if selected.Store == nil {
		return nil
	}
	selected = observationScope(ctx, selected)
	if selected.Thread == "" {
		return nil
	}
	input := recordtransactions.Input{Workspace: workspace, ReleaseSnapshot: releaseSnapshot}
	for call, item := range histories {
		input.Files = append(input.Files, legacycall.Name(workspace, call, false))
		input.Changes = append(input.Changes, item.ChangeID)
	}
	var items []map[string]json.RawMessage
	_ = json.Unmarshal(raw, &items)
	for _, item := range items {
		if responses.StringField(item, "type") == "message" {
			input.OptionalFiles = append(input.OptionalFiles, legacycall.Name(workspace, responses.StringField(item, "id"), true))
		}
	}
	for _, match := range retainedReadReference.FindAllStringSubmatch(string(raw), -1) {
		input.References = append(input.References, strings.Fields(strings.ReplaceAll(match[1], `\t`, " "))...)
	}
	if identity, _ := ctx.Value(storageSessionKey{}).(storageSessionIdentity); identity.Initialize {
		input.Initialize = &history.Scope{Thread: identity.Thread, Namespace: identity.Namespace, Parent: identity.Parent, Fork: identity.Fork}
	}
	err := recordtransactions.RetainInput(ctx, selected, source, recovered, input)
	if retention, ok := errors.AsType[*recordtransactions.RetentionError](err); ok {
		return requestexecution.StoragePublicationError(retention.Err)
	}
	return changeError(err)
}

// Only pressure queued for background maintenance is eligible for a persistence
// retry. Record-size limits and storage I/O errors cannot be cured by waiting.
// Keep completed response facts in memory while the existing maintenance worker
// frees space. Every attempt revalidates and publishes under store.lock; neither
// that lock nor a host operation is held or repeated while waiting.
func publishAfterMaintenance(ctx context.Context, selected changes.Scope, source changes.Service, publish func([]changes.Update), workspace string, histories map[string]mekugiHistory) error {
	err := publishObservations(ctx, selected, source, publish, workspace, histories)
	pressure, ok := errors.AsType[*history.PressureError](err)
	if !ok {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for {
		if waitErr := waitForStorageMaintenance(ctx, selected.Store, selected.Writes); waitErr != nil {
			return errors.Join(err, waitErr)
		}
		var used int64
		if checkErr := selected.Store.Transaction(ctx, selected.Writes, func(tx *history.Transaction) error {
			files, err := tx.FileSizes()
			if err == nil {
				used, _ = selected.Store.Usage(selected.Limits, files, pressure.Name, files[pressure.Name])
			}
			return err
		}); checkErr != nil {
			return errors.Join(err, checkErr)
		}
		// Measure reclamation before retrying: a batch may publish earlier
		// histories before reaching pressure again. Those successful writes
		// must not be mistaken for maintenance making no progress.
		if used >= pressure.Used {
			return err
		}
		err = publishObservations(ctx, selected, source, publish, workspace, histories)
		next, ok := errors.AsType[*history.PressureError](err)
		if !ok {
			return err
		}
		pressure = next
	}
}

func waitForStorageMaintenance(ctx context.Context, store *history.Store, writes *persistence.Counter) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		var pending bool
		err := store.Transaction(ctx, writes, func(tx *history.Transaction) error {
			var err error
			pending, err = tx.PendingCleanup()
			return err
		})
		if err != nil {
			return err
		}
		if !pending {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// Retain and admit prepared native metadata before publishing its bytes.
// Source: apps/mekugi/internal/router/storage_retention.go:217:226@[55257c48] writeManagedFile.
func writeRetainedRecord(tx *history.Transaction, thread string, limits history.Limits, name, pattern string, data []byte) error {
	if err := requestexecution.StoragePublicationError(tx.Retain(thread, limits, name)); err != nil {
		return requestexecution.StorageIOError(err)
	}
	if err := requestexecution.StoragePublicationError(tx.Admit(limits, name, int64(len(data)))); err != nil {
		return err
	}
	return requestexecution.StorageIOError(tx.Write(name, pattern, data))
}

// Age-based cleanup is router maintenance, not part of any request's replay
// view. An unrelated catalog must not prevent a new thread from starting.
type retentionScope struct {
	store  *history.Store
	writes *persistence.Counter
	limits history.Limits
	thread string
	notice func(string, string)
}

func runStorageRetention(ctx context.Context, diagnostics *diagnostic.Service, store func() retentionScope, notice func(error)) {
	var debugSweep, sessionSweep time.Time
	cleanup := func() {
		if diagnostics != nil && time.Since(debugSweep) >= time.Hour {
			debugSweep = time.Now()
			root, err := debugStorageDirectory()
			if err == nil {
				err = diagnostics.CleanupBundles(ctx, root, debugSweep)
			}
			if err != nil && ctx.Err() == nil && notice != nil {
				notice(fmt.Errorf("debug bundle cleanup: %w", err))
			}
		}
		s := store()
		if s.store == nil {
			return
		}
		var err error
		if time.Since(sessionSweep) < time.Minute {
			// Bound full age replanning when publications invalidate ownership.
			// Pressure still gets the worker's one-second response interval.
			var pressure bool
			err = s.store.Transaction(ctx, s.writes, func(tx *history.Transaction) error {
				var err error
				pressure, err = tx.PendingCleanup()
				return err
			})
			if err == nil && !pressure {
				return
			}
		}
		if err == nil {
			sessionSweep = time.Now()
			err = s.store.Cleanup(ctx, s.writes, s.limits, s.thread, s.notice)
		}
		if err != nil && ctx.Err() == nil && notice != nil {
			notice(fmt.Errorf("session cleanup: %w", err))
		}
	}
	cleanup()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cleanup()
		}
	}
}

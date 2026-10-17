package history

import (
	"bytes"
	"context"
	jsonv1 "encoding/json"
	"encoding/json/jsontext"
	"fmt"
	"maps"
	"reflect"
	"slices"

	"github.com/yusing/mekugi/libraries/legacycall"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

// HostConflict identifies a changed immutable translation or item field without
// retaining private payloads in the error. An empty Field is a translation conflict.
type HostConflict struct{ Field string }

// PublicationError distinguishes managed retention, admission and write failures
// from invalid retained facts or record preparation failures.
type PublicationError struct{ Err error }

func (e *PublicationError) Error() string { return e.Err.Error() }
func (e *PublicationError) Unwrap() error { return e.Err }

func (e *HostConflict) Error() string {
	if e.Field == "" {
		return "conflicting durable replay translation"
	}
	return fmt.Sprintf("conflicting durable replay item field %q", e.Field)
}

// ReadHostCall validates the selected historical envelope and its dependencies.
// Callers that join several owners use their existing publication transaction.
func (s *Store) ReadHostCall(workspace, callID string, commentary bool) (legacycall.Record, bool, error) {
	return legacycall.Read(s.directory, workspace, callID, commentary, MaxRecordBytes)
}

func (s *Store) LookupHostCall(ctx context.Context, workspace, callID string) (value legacycall.History, found bool, err error) {
	if s == nil {
		return
	}
	err = s.Transaction(ctx, nil, func(*Transaction) error {
		record, exists, readErr := s.ReadHostCall(workspace, callID, false)
		value, found = record.History, exists
		return readErr
	})
	return
}
func (s *Store) HasCommentary(ctx context.Context, workspace, id string) (found bool, err error) {
	if s == nil {
		return
	}
	err = s.Transaction(ctx, nil, func(*Transaction) error {
		_, exists, readErr := s.ReadHostCall(workspace, id, true)
		found = exists
		return readErr
	})
	return
}

// normalizeHostHistory removes representation differences already omitted by the
// frozen codec. Live watch/process facts cannot enter this neutral representation.
// Source: apps/mekugi/internal/router/mekugi_store.go:56:73@d002a0dc and capture durability.
func normalizeHostHistory(h legacycall.History) legacycall.History {
	normalizeInventory := func(value *legacycall.Inventory) *legacycall.Inventory {
		if value == nil {
			return nil
		}
		copy := *value
		if len(copy.Files) == 0 {
			copy.Files = nil
		}
		if len(copy.Entries) == 0 {
			copy.Entries = nil
		}
		if len(copy.Blobs) == 0 {
			copy.Blobs = nil
		}
		return &copy
	}
	if len(h.NativePatches) == 0 {
		h.NativePatches = nil
	}
	if h.ResolvedBaseline != nil {
		baseline := *h.ResolvedBaseline
		baseline.Inventory = normalizeInventory(baseline.Inventory)
		h.ResolvedBaseline = &baseline
	}
	if h.ExecObservation != nil {
		observation := *h.ExecObservation
		observation.WindowStart = observation.WindowStart.UTC()
		observation.Listings = slices.Clone(observation.Listings)
		for i := range observation.Listings {
			if len(observation.Listings[i].Entries) == 0 {
				observation.Listings[i].Entries = nil
			}
		}
		observation.Inventory = normalizeInventory(observation.Inventory)
		h.ExecObservation = &observation
	}
	return h
}

// mergeHostHistory accepts completion enrichment, never altered tool payloads.
// Source: apps/mekugi/internal/router/mekugi_store.go:165:219@d002a0dc.
func mergeHostHistory(old, next legacycall.History) (legacycall.History, error) {
	old, next = normalizeHostHistory(old), normalizeHostHistory(next)
	oldItem, nextItem := old.UpstreamItem, next.UpstreamItem
	oldIDs, nextIDs := old.CommentaryMessageIDs, next.CommentaryMessageIDs
	old.UpstreamItem, next.UpstreamItem = nil, nil
	old.CommentaryMessageIDs, next.CommentaryMessageIDs = nil, nil
	if !reflect.DeepEqual(old, next) {
		return next, &HostConflict{}
	}
	merged := make(map[string]jsonv1.RawMessage, len(oldItem)+len(nextItem))
	maps.Copy(merged, oldItem)
	for key, value := range nextItem {
		compact := jsontext.Value(bytes.Clone(value))
		if err := compact.Compact(); err != nil {
			return next, fmt.Errorf("invalid durable replay item field %q: %w", key, err)
		}
		if previous, exists := merged[key]; exists {
			before := jsontext.Value(bytes.Clone(previous))
			if err := before.Compact(); err != nil {
				return next, fmt.Errorf("invalid retained replay item field %q: %w", key, err)
			}
			metadata := key == "internal_chat_message_metadata_passthrough"
			finalized := key == "status" && string(before) == `"in_progress"` && (string(compact) == `"completed"` || string(compact) == `"incomplete"`)
			if !bytes.Equal(before, compact) && !metadata && !finalized {
				return next, &HostConflict{Field: key}
			}
		}
		merged[key] = jsonv1.RawMessage(compact)
	}
	if len(merged) > 0 {
		next.UpstreamItem = merged
	}
	next.CommentaryMessageIDs = slices.Clone(oldIDs)
	for _, id := range nextIDs {
		if !slices.Contains(next.CommentaryMessageIDs, id) {
			next.CommentaryMessageIDs = append(next.CommentaryMessageIDs, id)
		}
	}
	return next, nil
}

// PublishHostCall retains the envelope before exposing it to dependent owners.
// ordered is supplied by the evidence owner; history does not infer edit success.
// Source: apps/mekugi/internal/router/mekugi_store.go:220:255@d002a0dc.
func (tx *Transaction) PublishHostCall(thread string, limits Limits, r legacycall.Record, ordered bool) error {
	if err := tx.check(); err != nil {
		return err
	}
	r.History = normalizeHostHistory(r.History)
	previous, exists, err := tx.store.ReadHostCall(r.Workspace, r.CallID, r.Commentary)
	if err != nil {
		return err
	}
	if exists {
		r.CaptureOrder = previous.CaptureOrder
		r.History, err = mergeHostHistory(previous.History, r.History)
		if err != nil {
			return err
		}
		if reflect.DeepEqual(previous, r) {
			return nil
		}
	}
	if !exists && !r.Commentary && ordered {
		r.CaptureOrder, err = tx.ReserveCaptureOrder()
		if err != nil {
			return err
		}
	}
	data, err := responses.MarshalJSON(r)
	if err != nil {
		return err
	}
	if len(data) > MaxRecordBytes {
		return capacityError("replay record", int64(len(data)), MaxRecordBytes, "Split the tool call or reduce its retained output before retrying.")
	}
	name := legacycall.Name(r.Workspace, r.CallID, r.Commentary)
	prefix := "call-"
	if r.Commentary {
		prefix = "commentary-"
	}
	if err := tx.Retain(thread, limits, name); err != nil {
		return &PublicationError{Err: err}
	}
	if err := tx.Admit(limits, name, int64(len(data))); err != nil {
		return &PublicationError{Err: err}
	}
	if err := tx.Write(name, prefix+"pending-", data); err != nil {
		return &PublicationError{Err: err}
	}
	return nil
}

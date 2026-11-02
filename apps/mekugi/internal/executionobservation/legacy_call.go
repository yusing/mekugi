package executionobservation

import (
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/libraries/commandanalysis"
	"github.com/yusing/mekugi/libraries/commandanalysis/execscope"
	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/libraries/legacycall"
	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"
)

// The historical wire schema has no live preview or request-local state.
// Conversion leaves those fields unset, just as reading retained JSON did.
func legacyBaseline(v legacycall.Baseline) resolvedStockBaseline {
	return resolvedStockBaseline{
		Root:    v.Root,
		Shell:   v.Shell,
		Files:   legacySlice(v.Files, legacyFile),
		Omitted: legacySlice(v.Omitted, func(v legacycall.Omission) changecapture.Omission { return changecapture.Omission(v) }),
	}
}

func legacyFile(v legacycall.File) changecapture.File {
	return changecapture.File{
		Origin:      v.Origin,
		AlsoManaged: v.AlsoManaged,
		Path:        v.Path,
		Kind:        changecapture.FileKind(v.Kind),
		Content:     v.Content,
		Size:        v.Size,
		Hash:        v.Hash,
		Link:        v.Link,
		Stamp:       v.Stamp,
		Error:       v.Error,
		CopyOf:      v.CopyOf,
	}
}

func legacyHistory(v legacycall.History) mekugiHistory {
	result := mekugiHistory{
		ToolName:             v.ToolName,
		PluginID:             v.PluginID,
		Script:               v.Script,
		Root:                 v.Root,
		ExecutingThread:      v.ExecutingThread,
		Caller:               v.Caller,
		Source:               v.Source,
		ChangeID:             v.ChangeID,
		ReviewFiles:          legacySlice(v.ReviewFiles, func(v legacycall.ReviewFile) diffmodel.ReviewFile { return diffmodel.ReviewFile(v) }),
		NativePatches:        legacySlice(v.NativePatches, legacyNativePatch),
		ResolvedBaseline:     legacyPointer(v.ResolvedBaseline, legacyBaseline),
		ExecObservation:      legacyPointer(v.ExecObservation, legacyObservation),
		ExecOutcome:          legacyPointer(v.ExecOutcome, legacyOutcome),
		HostResults:          legacySlice(v.HostResults, func(v legacycall.HostResult) binding.ToolResult { return binding.ToolResult(v) }),
		CommandSegments:      v.CommandSegments,
		CarrierName:          v.CarrierName,
		CarrierKind:          codeModeCarrierKind(v.CarrierKind),
		CarrierPayload:       v.CarrierPayload,
		Report:               v.Report,
		OutputWarning:        v.OutputWarning,
		TranslationError:     v.TranslationError,
		CorrelationID:        v.CorrelationID,
		Attempt:              v.Attempt,
		UpstreamItem:         v.UpstreamItem,
		ReplayCarrier:        v.ReplayCarrier,
		CommentaryMessageIDs: v.CommentaryMessageIDs,
		AlreadySatisfied:     v.AlreadySatisfied,
	}
	result.legacy = legacyMetadata(v)
	return result
}

func legacyMetadata(v legacycall.History) *legacycall.History {
	if v.ExecObservation != nil && v.ExecObservation.Inventory != nil ||
		v.ResolvedBaseline != nil && v.ResolvedBaseline.Inventory != nil ||
		v.ExecOutcome != nil && v.ExecOutcome.Unswept != "" {
		return new(v)
	}
	return nil
}

func legacyNativePatch(v legacycall.NativePatch) changecapture.PatchObservation {
	return changecapture.PatchObservation{
		Input: v.Input,
		Files: legacySlice(v.Files, func(v legacycall.NativeFile) changecapture.PatchFile { return changecapture.PatchFile(v) }),
	}
}

func legacyObservation(v legacycall.Observation) changecapture.Observation {
	return changecapture.Observation{
		Commands:       legacySlice(v.Commands, func(v legacycall.Command) commandanalysis.Command { return commandanalysis.Command(v) }),
		CommandClasses: v.CommandClasses,
		Class:          v.Class,
		Labels:         v.Labels,
		Reason:         v.Reason,
		Files:          legacySlice(v.Files, legacyFile),
		Omitted:        legacySlice(v.Omitted, func(v legacycall.Omission) changecapture.Omission { return changecapture.Omission(v) }),
		Listings:       legacySlice(v.Listings, func(v legacycall.Listing) changecapture.Listing { return changecapture.Listing(v) }),
		CodeMode:       v.CodeMode,
		Roots:          v.Roots,
		WindowStart:    v.WindowStart,
		Programs:       legacySlice(v.Programs, func(v legacycall.Program) execscope.Program { return execscope.Program(v) }),
		Group:          v.Group,
		Excluded:       v.Excluded,
		RepeatedPaths:  v.RepeatedPaths,
		LiteralClass:   v.LiteralClass,
		Tree:           v.Tree,
	}
}

func legacySlice[A, B any](values []A, convert func(A) B) []B {
	if values == nil {
		return nil
	}
	result := make([]B, len(values))
	for i, value := range values {
		result[i] = convert(value)
	}
	return result
}

func legacyPointer[A, B any](value *A, convert func(A) B) *B {
	if value == nil {
		return nil
	}
	return new(convert(*value))
}

// Typed neutral publication and cache conversion; live-only fields stay local.
// Source: apps/mekugi/internal/router/legacy_call.go:14:143@d002a0dc, inverse conversion.
func retainedBaseline(v resolvedStockBaseline) legacycall.Baseline {
	return legacycall.Baseline{
		Root:    v.Root,
		Shell:   v.Shell,
		Files:   legacySlice(v.Files, retainedFile),
		Omitted: legacySlice(v.Omitted, func(v changecapture.Omission) legacycall.Omission { return legacycall.Omission(v) }),
	}
}

func retainedFile(v changecapture.File) legacycall.File {
	return legacycall.File{
		Origin:      v.Origin,
		AlsoManaged: v.AlsoManaged,
		Path:        v.Path,
		Kind:        string(v.Kind),
		Content:     v.Content,
		Size:        v.Size,
		Hash:        v.Hash,
		Link:        v.Link,
		Stamp:       v.Stamp,
		Error:       v.Error,
		CopyOf:      v.CopyOf,
	}
}

func retainedHistory(v mekugiHistory) legacycall.History {
	result := legacycall.History{
		ToolName:             v.ToolName,
		PluginID:             v.PluginID,
		Script:               v.Script,
		Root:                 v.Root,
		ExecutingThread:      v.ExecutingThread,
		Caller:               v.Caller,
		Source:               v.Source,
		ChangeID:             v.ChangeID,
		ReviewFiles:          legacySlice(v.ReviewFiles, func(v diffmodel.ReviewFile) legacycall.ReviewFile { return legacycall.ReviewFile(v) }),
		NativePatches:        legacySlice(v.NativePatches, retainedNativePatch),
		ResolvedBaseline:     legacyPointer(v.ResolvedBaseline, retainedBaseline),
		ExecObservation:      legacyPointer(v.ExecObservation, retainedObservation),
		ExecOutcome:          legacyPointer(v.ExecOutcome, retainedOutcome),
		HostResults:          legacySlice(v.HostResults, func(v binding.ToolResult) legacycall.HostResult { return legacycall.HostResult(v) }),
		CommandSegments:      v.CommandSegments,
		CarrierName:          v.CarrierName,
		CarrierKind:          string(v.CarrierKind),
		CarrierPayload:       v.CarrierPayload,
		Report:               v.Report,
		OutputWarning:        v.OutputWarning,
		TranslationError:     v.TranslationError,
		CorrelationID:        v.CorrelationID,
		Attempt:              v.Attempt,
		UpstreamItem:         v.UpstreamItem,
		ReplayCarrier:        v.ReplayCarrier,
		CommentaryMessageIDs: v.CommentaryMessageIDs,
		AlreadySatisfied:     v.AlreadySatisfied,
	}
	// Frozen metadata belongs to the historical record, not live capture.
	if old := v.legacy; old != nil {
		if old.ExecObservation != nil && result.ExecObservation != nil {
			result.ExecObservation.Inventory = old.ExecObservation.Inventory
		}
		if old.ResolvedBaseline != nil && result.ResolvedBaseline != nil {
			result.ResolvedBaseline.Inventory = old.ResolvedBaseline.Inventory
		}
		if old.ExecOutcome != nil && old.ExecOutcome.Unswept != "" {
			if result.ExecOutcome == nil {
				result.ExecOutcome = new(legacycall.Outcome)
			}
			result.ExecOutcome.Unswept = old.ExecOutcome.Unswept
		}
	}
	return result
}

func retainedNativePatch(v changecapture.PatchObservation) legacycall.NativePatch {
	return legacycall.NativePatch{
		Input: v.Input,
		Files: legacySlice(v.Files, func(v changecapture.PatchFile) legacycall.NativeFile { return legacycall.NativeFile(v) }),
	}
}

func retainedObservation(v changecapture.Observation) legacycall.Observation {
	return legacycall.Observation{
		Commands:       legacySlice(v.Commands, func(v commandanalysis.Command) legacycall.Command { return legacycall.Command(v) }),
		CommandClasses: v.CommandClasses,
		Class:          v.Class,
		Labels:         v.Labels,
		Reason:         v.Reason,
		Files:          legacySlice(v.Files, retainedFile),
		Omitted:        legacySlice(v.Omitted, func(v changecapture.Omission) legacycall.Omission { return legacycall.Omission(v) }),
		Listings:       legacySlice(v.Listings, func(v changecapture.Listing) legacycall.Listing { return legacycall.Listing(v) }),
		CodeMode:       v.CodeMode,
		Roots:          v.Roots,
		WindowStart:    v.WindowStart,
		Programs:       legacySlice(v.Programs, func(v execscope.Program) legacycall.Program { return legacycall.Program(v) }),
		Group:          v.Group,
		Excluded:       v.Excluded,
		RepeatedPaths:  v.RepeatedPaths,
		LiteralClass:   v.LiteralClass,
		Tree:           v.Tree,
	}
}

func legacyOutcome(v legacycall.Outcome) changecapture.Outcome {
	return changecapture.Outcome{
		DeletedDirectories: v.DeletedDirectories,
		OutputRef:          v.OutputRef,
		Status:             v.Status,
		Exit:               v.Exit,
		Class:              v.Class,
		Labels:             v.Labels,
		Coverage:           v.Coverage,
		CodeMode:           v.CodeMode,
		Scope:              v.Scope,
		ScopeReason:        v.ScopeReason,
		Overlaps:           v.Overlaps,
		Background:         v.Background,
		SharedWith:         v.SharedWith,
	}
}

func retainedOutcome(v changecapture.Outcome) legacycall.Outcome {
	return legacycall.Outcome{
		DeletedDirectories: v.DeletedDirectories,
		OutputRef:          v.OutputRef,
		Status:             v.Status,
		Exit:               v.Exit,
		Class:              v.Class,
		Labels:             v.Labels,
		Coverage:           v.Coverage,
		CodeMode:           v.CodeMode,
		Scope:              v.Scope,
		ScopeReason:        v.ScopeReason,
		Overlaps:           v.Overlaps,
		Background:         v.Background,
		SharedWith:         v.SharedWith,
	}
}

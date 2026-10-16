package router

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
		Root:      v.Root,
		Shell:     v.Shell,
		Files:     legacySlice(v.Files, legacyFile),
		Omitted:   legacySlice(v.Omitted, func(v legacycall.Omission) changecapture.Omission { return changecapture.Omission(v) }),
		Inventory: legacyPointer(v.Inventory, legacyInventory),
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
	return mekugiHistory{
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
		ExecOutcome:          legacyPointer(v.ExecOutcome, func(v legacycall.Outcome) changecapture.Outcome { return changecapture.Outcome(v) }),
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
}

func legacyInventory(v legacycall.Inventory) changecapture.Inventory {
	return changecapture.Inventory{
		Version:      v.Version,
		Directories:  legacyDirectories(v.Directories),
		Root:         v.Root,
		Entries:      v.Entries,
		Blobs:        v.Blobs,
		Files:        legacySlice(v.Files, legacyFile),
		Repositories: v.Repositories,
		Pruned:       v.Pruned,
		Omitted:      legacySlice(v.Omitted, func(v legacycall.Omission) changecapture.Omission { return changecapture.Omission(v) }),
	}
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
		Inventory:      legacyPointer(v.Inventory, legacyInventory),
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

func legacyDirectories(values map[string]legacycall.DirectoryStamp) map[string]changecapture.DirectoryStamp {
	if values == nil {
		return nil
	}
	result := make(map[string]changecapture.DirectoryStamp, len(values))
	for key, value := range values {
		result[key] = changecapture.DirectoryStamp(value)
	}
	return result
}

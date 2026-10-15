package router

import (
	"github.com/yusing/mekugi"
	"github.com/yusing/mekugi/internal/execscope"
	"github.com/yusing/mekugi/internal/execsegment"
	"github.com/yusing/mekugi/libraries/legacycall"
)

// The historical wire schema has no live preview or request-local state.
// Conversion leaves those fields unset, just as reading retained JSON did.
func legacyBaseline(v legacycall.Baseline) resolvedStockBaseline {
	return resolvedStockBaseline{
		Root:      v.Root,
		Shell:     v.Shell,
		Files:     legacySlice(v.Files, legacyFile),
		Omitted:   legacySlice(v.Omitted, func(v legacycall.Omission) execOmission { return execOmission(v) }),
		Inventory: legacyPointer(v.Inventory, legacyInventory),
	}
}

func legacyFile(v legacycall.File) execFileSnapshot {
	return execFileSnapshot{
		Origin:      v.Origin,
		AlsoManaged: v.AlsoManaged,
		Path:        v.Path,
		Kind:        execFileKind(v.Kind),
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
		ReviewFiles:          legacySlice(v.ReviewFiles, func(v legacycall.ReviewFile) mekugi.ReviewFile { return mekugi.ReviewFile(v) }),
		NativePatches:        legacySlice(v.NativePatches, legacyNativePatch),
		ResolvedBaseline:     legacyPointer(v.ResolvedBaseline, legacyBaseline),
		ExecObservation:      legacyPointer(v.ExecObservation, legacyObservation),
		ExecOutcome:          legacyPointer(v.ExecOutcome, func(v legacycall.Outcome) execOutcome { return execOutcome(v) }),
		HostResults:          legacySlice(v.HostResults, func(v legacycall.HostResult) nativeToolResult { return nativeToolResult(v) }),
		CommandSegments:      legacyPointer(v.CommandSegments, legacySegments),
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

func legacyInventory(v legacycall.Inventory) execInventory {
	return execInventory{
		Version:      v.Version,
		Directories:  legacyDirectories(v.Directories),
		Root:         v.Root,
		Entries:      v.Entries,
		Blobs:        v.Blobs,
		Files:        legacySlice(v.Files, legacyFile),
		Repositories: v.Repositories,
		Pruned:       v.Pruned,
		Omitted:      legacySlice(v.Omitted, func(v legacycall.Omission) execOmission { return execOmission(v) }),
	}
}

func legacyNativePatch(v legacycall.NativePatch) nativePatchObservation {
	return nativePatchObservation{
		Input: v.Input,
		Files: legacySlice(v.Files, func(v legacycall.NativeFile) nativePatchFileSnapshot { return nativePatchFileSnapshot(v) }),
	}
}

func legacyObservation(v legacycall.Observation) execObservation {
	return execObservation{
		Commands:       legacySlice(v.Commands, func(v legacycall.Command) execCommandInput { return execCommandInput(v) }),
		CommandClasses: v.CommandClasses,
		Class:          v.Class,
		Labels:         v.Labels,
		Reason:         v.Reason,
		Files:          legacySlice(v.Files, legacyFile),
		Omitted:        legacySlice(v.Omitted, func(v legacycall.Omission) execOmission { return execOmission(v) }),
		Listings:       legacySlice(v.Listings, func(v legacycall.Listing) execListing { return execListing(v) }),
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

func legacySegment(v legacycall.Segment) retainedCommandSegment {
	return retainedCommandSegment{
		Timing:  execsegment.Timing(v.Timing),
		Source:  v.Source,
		Skipped: v.Skipped,
		Exit:    v.Exit,
		Output:  v.Output,
	}
}

func legacySegments(v legacycall.Segments) retainedCommandSegments {
	return retainedCommandSegments{
		Command: v.Command,
		Exit:    v.Exit,
		Output:  v.Output,
		Parts:   legacySlice(v.Parts, legacySegment),
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

func legacyDirectories(values map[string]legacycall.DirectoryStamp) map[string]execDirectoryStamp {
	if values == nil {
		return nil
	}
	result := make(map[string]execDirectoryStamp, len(values))
	for key, value := range values {
		result[key] = execDirectoryStamp(value)
	}
	return result
}

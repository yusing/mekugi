package executionobservation

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/contracts/command"
)

const nativeProgressBytes = 12 << 10

type nativeProgressResource struct {
	Version                                  int
	Root, Caller, Work, Wait, Batch, Context string
	Frozen                                   appserver.ProgressFreezeResponse
}

// FreezeNativeProgress borrows one immutable, unfiltered publication for the
// native-selected actors. It neither selects work nor advances capture cursors.
func (p *Preparer) FreezeNativeProgress(ctx context.Context, input appserver.ProgressFreezeRequest) (appserver.ProgressFreezeResponse, error) {
	var result appserver.ProgressFreezeResponse
	reader, retained, err := p.nativeCaptureScope(input.RootThreadID, input.CallerThreadID)
	if err != nil {
		return result, err
	}
	if input.CallerWorkID == "" || input.WaitID == "" || input.BatchID == "" || input.RecipientContextID == "" {
		return result, errors.New("native progress has incomplete batch identity")
	}
	seen, err := nativeCaptureCursors(input.Seen)
	if err != nil {
		return result, err
	}
	callers := make(map[string]bool, len(input.Selected))
	actors := make(map[string]appserver.ProgressActor, len(input.Selected))
	threads := make(map[string]bool, len(input.Selected))
	for _, actor := range input.Selected {
		if actor.WorkID == "" || (actor.TaskName != "/root" && !strings.HasPrefix(actor.TaskName, "/root/")) || callers[actor.TaskName] || threads[actor.ThreadID] {
			return result, errors.New("native progress requires unique admitted actors and canonical capture caller names")
		}
		scope, err := reader.Scope.Store.ReadAdmittedScope(actor.ThreadID)
		if err != nil {
			return result, err
		}
		if scope.Namespace != reader.Scope.Namespace {
			return result, errors.New("native progress actor is outside the admitted root namespace")
		}
		callers[actor.TaskName], threads[actor.ThreadID], actors[actor.TaskName] = true, true, actor
	}
	result.Coverage = appserver.CaptureCoverage{Status: "unavailable", Diagnostics: []string{"Captured progress is unavailable"}}
	if p.ProgressSnapshot != nil {
		if view := p.ProgressSnapshot(input.RootThreadID); view != nil {
			result.PublicationRevision = view.Revision
			result.Coverage.Status, result.Coverage.Diagnostics = view.Coverage, nil
			if result.Coverage.Status == "" {
				result.Coverage.Status = "unavailable"
			}
			if view.Failure != "" {
				result.Coverage.Diagnostics = []string{view.Failure}
			} else if result.Coverage.Status != "complete" {
				result.Coverage.Diagnostics = []string{"Capture coverage: " + result.Coverage.Status}
			}
			prior := make(map[string]int, len(seen))
			for key, count := range seen {
				prior[key] = int(min(count, uint64(^uint(0)>>1)))
			}
			indices := make(map[string]int)
			for capture := range view.Publication.Progress(callers, prior) {
				index, exists := indices[capture.Caller]
				if !exists {
					index = len(result.Agents)
					indices[capture.Caller] = index
					result.Agents = append(result.Agents, appserver.FrozenAgentCapture{Actor: actors[capture.Caller]})
				}
				result.Agents[index].Intervals = append(result.Agents[index].Intervals, appserver.CaptureInterval{
					Key: capture.Key, ChangeID: capture.Change, RecordOrder: capture.RecordOrder, LegacyRecordKey: capture.LegacyRecordKey,
					FirstChunk: capture.FirstChunk, LastChunk: uint64(capture.Chunks),
				})
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return appserver.ProgressFreezeResponse{}, err
	}
	if len(result.Agents) == 0 && result.Coverage.Status == "complete" && len(result.Coverage.Diagnostics) == 0 {
		return result, nil
	}
	resource := nativeProgressResource{Version: 1, Root: input.RootThreadID, Caller: input.CallerThreadID, Work: input.CallerWorkID,
		Wait: input.WaitID, Batch: input.BatchID, Context: input.RecipientContextID, Frozen: result}
	result.SnapshotRef, err = publishNativeResource(ctx, retained, &resource)
	return result, err
}

func sameCaptureInterval(a, b appserver.CaptureInterval) bool {
	return a.Key == b.Key && a.ChangeID == b.ChangeID && a.RecordOrder == b.RecordOrder && a.FirstChunk == b.FirstChunk && a.LastChunk == b.LastChunk &&
		(a.LegacyRecordKey == nil && b.LegacyRecordKey == nil || a.LegacyRecordKey != nil && b.LegacyRecordKey != nil && *a.LegacyRecordKey == *b.LegacyRecordKey)
}

// PrepareNativeProgressOutput validates the exact frozen intervals before
// bounding combined text. Recovery-only output acknowledges no capture evidence.
func (p *Preparer) PrepareNativeProgressOutput(ctx context.Context, input appserver.ProgressOutputRequest) (appserver.ProgressOutputResponse, error) {
	var result appserver.ProgressOutputResponse
	_, retained, err := p.nativeCaptureScope(input.RootThreadID, input.CallerThreadID)
	if err != nil {
		return result, err
	}
	var resource nativeProgressResource
	if err := readNativeResource(retained, input.SnapshotRef, &resource); err != nil {
		return result, err
	}
	if resource.Version != 1 || resource.Root != input.RootThreadID || resource.Caller != input.CallerThreadID || resource.Work != input.CallerWorkID ||
		resource.Wait != input.WaitID || resource.Batch != input.BatchID || resource.Context != input.RecipientContextID {
		return result, errors.New("progress capture resource identity mismatch")
	}
	if input.Coverage.Status != resource.Frozen.Coverage.Status || !slices.Equal(input.Coverage.Diagnostics, resource.Frozen.Coverage.Diagnostics) || len(input.Agents) != len(resource.Frozen.Agents) {
		return result, errors.New("progress output changed its frozen capture selection or coverage")
	}
	var body strings.Builder
	for i, agent := range input.Agents {
		frozen := resource.Frozen.Agents[i]
		if agent.Actor != frozen.Actor || !slices.EqualFunc(agent.Intervals, frozen.Intervals, sameCaptureInterval) {
			return result, errors.New("progress output changed its frozen actor or intervals")
		}
		fmt.Fprintf(&body, "%s\n", agent.Actor.TaskName)
		switch agent.Summary.Status {
		case "available":
			if strings.TrimSpace(agent.Summary.Text) == "" || agent.Summary.Reason != "" {
				return result, errors.New("invalid available progress summary")
			}
			body.WriteString(agent.Summary.Text)
			body.WriteByte('\n')
			result.DeliveredIntervals = append(result.DeliveredIntervals, agent.Intervals...)
		case "unavailable":
			if strings.TrimSpace(agent.Summary.Reason) == "" || agent.Summary.Text != "" {
				return result, errors.New("invalid unavailable progress summary")
			}
			fmt.Fprintf(&body, "Summary unavailable: %s\n", agent.Summary.Reason)
		default:
			return result, errors.New("unknown native progress summary result")
		}
		var ids []string
		selected := make(map[string]bool)
		for _, interval := range agent.Intervals {
			if !selected[interval.ChangeID] {
				selected[interval.ChangeID] = true
				ids = append(ids, interval.ChangeID)
			}
		}
		fmt.Fprintf(&body, "mchanges:%s\nFrozen intervals: ", strings.Join(ids, ","))
		for i, interval := range agent.Intervals {
			if i != 0 {
				body.WriteString("; ")
			}
			if interval.LegacyRecordKey == nil {
				fmt.Fprintf(&body, "%s record=%d chunks=%d..%d", interval.ChangeID, interval.RecordOrder, interval.FirstChunk, interval.LastChunk)
			} else {
				fmt.Fprintf(&body, "%s legacy=%q chunks=%d..%d", interval.ChangeID, *interval.LegacyRecordKey, interval.FirstChunk, interval.LastChunk)
			}
		}
		body.WriteByte('\n')
	}
	for _, diagnostic := range input.Coverage.Diagnostics {
		body.WriteString(diagnostic)
		body.WriteByte('\n')
	}
	if err := ctx.Err(); err != nil {
		return appserver.ProgressOutputResponse{}, err
	}
	result.Text = body.String()
	if len(result.Text) <= nativeProgressBytes {
		return result, nil
	}
	ref, err := retained.Publish(ctx, command.OmittedOutput{Stdout: result.Text}, 0)
	if err != nil {
		return appserver.ProgressOutputResponse{}, err
	}
	return appserver.ProgressOutputResponse{Text: "Progress output exceeds 12 KiB. Read the complete frozen summaries and intervals with mread " + ref + ".", RecoveryRef: &ref}, nil
}

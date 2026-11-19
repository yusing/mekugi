package executionobservation

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"strings"

	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/modules/agentguidance/compact"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	"github.com/yusing/mekugi/modules/outputrecovery/recovery"
)

type nativeFrozenSource struct {
	Command string
	Capture *changes.FrozenRead
	Legacy  *changes.LegacyFrozenRead `json:"-"`
	Error   string
}

type nativeFinalResource struct {
	Version                                       int
	Root, Thread, Operation, Recipient, Workspace string
	Sources                                       []nativeFrozenSource
}

// nativeCaptureScope borrows existing resource owners, never a logical tree.
func (p *Preparer) nativeCaptureScope(root, recipient string) (compact.ChangeReader, *recovery.Session, error) {
	if p == nil || p.Retained.Store == nil || p.Recovery == nil {
		return compact.ChangeReader{}, nil, errors.New("native capture storage is unavailable")
	}
	reader, retained, err := p.completionReader(recipient)
	if err != nil {
		return reader, nil, err
	}
	admitted, err := reader.Scope.Store.ReadAdmittedScope(root)
	if err != nil {
		return reader, nil, err
	}
	if admitted.Namespace != reader.Scope.Namespace {
		return reader, nil, errors.New("capture recipient is outside the admitted root namespace")
	}
	return reader, retained, nil
}

func publishNativeResource(ctx context.Context, retained *recovery.Session, resource any) (string, error) {
	data, err := json.Marshal(resource)
	if err != nil {
		return "", err
	}
	return retained.Publish(ctx, command.OmittedOutput{Stdout: string(data)}, 0)
}

// readNativeResource borrows retained bytes once. Version-specific readers keep
// historical formats separate from the metadata-only writer.
func readNativeResource(retained *recovery.Session, ref string) (jsontext.Value, int, error) {
	output, err := retained.Snapshot(ref)
	if err != nil {
		return nil, 0, err
	}
	if output.ExitCode != 0 || output.Stderr != "" {
		return nil, 0, errors.New("invalid native capture resource")
	}
	data := jsontext.Value(output.Stdout)
	var header struct{ Version int }
	if err := json.Unmarshal(data, &header); err != nil {
		return nil, 0, err
	}
	return data, header.Version, nil
}

// Only historical readers use Work/FullText and copied captures. Their former
// coordinator identity grants no authority; the retained recipient binding does.
type legacyNativeFinalResource struct {
	Version                                            int
	Root, Thread, Work, Recipient, Workspace, FullText string
	Sources                                            []struct {
		Command string
		Capture *changes.LegacyFrozenRead
		Error   string
	}
}

func nativeCaptureCursors(cursors []appserver.CaptureCursor) (map[string]uint64, error) {
	seen := make(map[string]uint64, len(cursors))
	for _, cursor := range cursors {
		if _, exists := seen[cursor.Key]; cursor.Key == "" || exists {
			return nil, errors.New("invalid or duplicate native capture cursor")
		}
		seen[cursor.Key] = cursor.ThroughChunk
	}
	return seen, nil
}

// PrepareNativeFinalCapture freezes exact retained ranges. The resource contains
// selection metadata only; capture storage retains the evidence dependencies.
func (p *Preparer) PrepareNativeFinalCapture(ctx context.Context, input appserver.FinalCaptureRequest) (*appserver.FrozenFinalCapture, error) {
	if len(input.Sources) == 0 {
		return nil, nil
	}
	fail := func(err error) (*appserver.FrozenFinalCapture, error) {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return &appserver.FrozenFinalCapture{FullText: fmt.Sprintf("CONTENT NOT ATTACHED: %v\n", err)}, nil
	}
	reader, retained, err := p.nativeCaptureScope(input.RootThreadID, input.RecipientThreadID)
	if err != nil {
		return fail(err)
	}
	if input.RecipientCwd == nil || *input.RecipientCwd == "" {
		return fail(errors.New("completion recipient workspace is unavailable"))
	}
	if reader.Service == nil {
		return fail(errors.New("completion capture reader is unavailable"))
	}
	selection, err := json.Marshal(&struct {
		Sources []string `json:"sources"`
	}{input.Sources})
	if err != nil {
		return fail(err)
	}
	if err := compact.CompletionSources(string(selection)); err != nil {
		return fail(err)
	}
	sources, err := compact.ReloadSources(string(selection))
	if err != nil {
		return fail(err)
	}
	commands, err := compact.ReadCommands(string(selection))
	if err != nil {
		return fail(err)
	}
	resource := nativeFinalResource{Version: 2, Root: input.RootThreadID, Thread: input.ThreadID, Operation: input.OperationID, Recipient: input.RecipientThreadID, Workspace: *input.RecipientCwd}
	for i, source := range sources {
		frozen := nativeFrozenSource{Command: commands[i]}
		query, err := reader.Service.Parse(source.Changes, resource.Workspace)
		if err == nil {
			query.OmitOwn = true
			var capture changes.FrozenRead
			capture, err = reader.Service.Freeze(ctx, reader.Scope, query)
			if err == nil {
				if len(capture.Captures) == 0 && capture.Diagnostic == "" && capture.Fallback == "" {
					continue
				}
				frozen.Capture = &capture
			}
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			frozen.Error = err.Error()
		}
		resource.Sources = append(resource.Sources, frozen)
	}
	if len(resource.Sources) == 0 {
		return nil, nil
	}
	fullText, err := p.renderNativeFinal(ctx, reader.Scope, resource, nil, retained)
	if err != nil {
		return fail(err)
	}
	ref, err := publishNativeResource(ctx, retained, &resource)
	if err != nil {
		return fail(err)
	}
	return &appserver.FrozenFinalCapture{ResourceRef: ref, FullText: fullText}, nil
}

func (p *Preparer) ProjectNativeFinalCapture(ctx context.Context, input appserver.FinalCaptureProjectRequest) (appserver.FinalCaptureProjectResponse, error) {
	var result appserver.FinalCaptureProjectResponse
	reader, retained, err := p.nativeCaptureScope(input.RootThreadID, input.RecipientThreadID)
	if err != nil {
		return result, err
	}
	data, version, err := readNativeResource(retained, input.ResourceRef)
	if err != nil {
		return result, err
	}
	var resource nativeFinalResource
	legacyText := ""
	switch version {
	case 1:
		var legacy legacyNativeFinalResource
		if err := json.Unmarshal(data, &legacy, json.RejectUnknownMembers(true)); err != nil {
			return result, err
		}
		resource = nativeFinalResource{Version: 2, Root: legacy.Root, Thread: legacy.Thread, Operation: input.OperationID, Recipient: legacy.Recipient, Workspace: legacy.Workspace}
		legacyText = legacy.FullText
		for _, source := range legacy.Sources {
			resource.Sources = append(resource.Sources, nativeFrozenSource{Command: source.Command, Legacy: source.Capture, Error: source.Error})
		}
	case 2:
		if err := json.Unmarshal(data, &resource, json.RejectUnknownMembers(true)); err != nil {
			return result, err
		}
	default:
		return result, errors.New("unsupported final capture resource version")
	}
	if resource.Version != 2 || resource.Root != input.RootThreadID || resource.Thread != input.ThreadID || resource.Operation != input.OperationID || resource.Recipient != input.RecipientThreadID ||
		input.RecipientCwd == nil || resource.Workspace != *input.RecipientCwd {
		return result, errors.New("final capture resource identity mismatch")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	seen, err := nativeCaptureCursors(input.Seen)
	if err != nil {
		return result, err
	}
	if input.RecipientContextID == "" {
		seen = nil
	}
	if len(seen) == 0 && legacyText != "" {
		result.Text = legacyText
		return result, nil
	}
	result.Text, err = p.renderNativeFinal(ctx, reader.Scope, resource, seen, retained)
	return result, err
}

func (p *Preparer) renderNativeFinal(ctx context.Context, selected changes.Scope, resource nativeFinalResource, seen map[string]uint64, retained *recovery.Session) (string, error) {
	var body strings.Builder
	if len(seen) != 0 {
		body.WriteString("Captures already reported by yield summaries omitted; explicit mchanges reads retain full diffs.\n")
	}
	result := compact.CapturedSource{}
	var sources []completionSource
	for _, source := range resource.Sources {
		output := compact.CapturedSource{Command: source.Command}
		var projection changes.ProjectedCapture
		var err error
		if source.Capture != nil || source.Legacy != nil {
			if p.Changes == nil {
				return "", errors.New("completion capture projector is unavailable")
			}
			if source.Legacy != nil {
				projection, err = p.Changes.ProjectLegacyFrozen(ctx, *source.Legacy, seen)
			} else {
				projection, err = p.Changes.ProjectFrozen(ctx, selected, *source.Capture, seen)
			}
		} else {
			err = errors.New(source.Error)
		}
		if err != nil {
			output.ExitCode, output.Stderr = 1, "mchanges: "+err.Error()+"\n"
		}
		output.Stdout = projection.Text
		if projection.Diff {
			start := body.Len() + len((compact.CapturedSource{Command: output.Command, ExitCode: output.ExitCode}).Text())
			sources = append(sources, completionSource{command: source.Command, start: start, end: start + len(output.Stdout)})
		}
		body.WriteString(output.Text())
		body.WriteByte('\n')
		result.ExitCode = max(result.ExitCode, output.ExitCode)
	}
	result.Stdout = body.String()
	return boundCompletion(ctx, result, sources, retained)
}

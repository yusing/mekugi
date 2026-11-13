package executionobservation

import (
	"context"
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
	Error   string
}

type nativeFinalResource struct {
	Version                                            int
	Root, Thread, Work, Recipient, Workspace, FullText string
	Sources                                            []nativeFrozenSource
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

func readNativeResource(retained *recovery.Session, ref string, resource any) error {
	output, err := retained.Snapshot(ref)
	if err != nil {
		return err
	}
	if output.ExitCode != 0 || output.Stderr != "" {
		return errors.New("invalid native capture resource")
	}
	return json.Unmarshal([]byte(output.Stdout), resource, json.RejectUnknownMembers(true))
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

// PrepareNativeFinalCapture freezes full capture bytes once. Later projections
// read only this retained resource, including after a recipient context reset.
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
	resource := nativeFinalResource{Version: 1, Root: input.RootThreadID, Thread: input.ThreadID, Work: input.WorkID, Recipient: input.RecipientThreadID, Workspace: *input.RecipientCwd}
	for i, source := range sources {
		frozen := nativeFrozenSource{Command: commands[i]}
		query, err := reader.Service.Parse(source.Changes, resource.Workspace)
		if err == nil {
			var capture changes.FrozenRead
			capture, err = reader.Service.Freeze(ctx, reader.Scope, query)
			if err == nil {
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
	resource.FullText, err = p.renderNativeFinal(ctx, resource, nil, retained)
	if err != nil {
		return fail(err)
	}
	ref, err := publishNativeResource(ctx, retained, &resource)
	if err != nil {
		return fail(err)
	}
	return &appserver.FrozenFinalCapture{ResourceRef: ref, FullText: resource.FullText}, nil
}

func (p *Preparer) ProjectNativeFinalCapture(ctx context.Context, input appserver.FinalCaptureProjectRequest) (appserver.FinalCaptureProjectResponse, error) {
	var result appserver.FinalCaptureProjectResponse
	_, retained, err := p.nativeCaptureScope(input.RootThreadID, input.RecipientThreadID)
	if err != nil {
		return result, err
	}
	var resource nativeFinalResource
	if err := readNativeResource(retained, input.ResourceRef, &resource); err != nil {
		return result, err
	}
	if resource.Version != 1 || resource.Root != input.RootThreadID || resource.Thread != input.ThreadID || resource.Work != input.WorkID || resource.Recipient != input.RecipientThreadID ||
		input.RecipientCwd == nil || resource.Workspace != *input.RecipientCwd {
		return result, errors.New("final capture resource identity mismatch")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if input.RecipientContextID == "" || len(input.Seen) == 0 {
		result.Text = resource.FullText
		return result, nil
	}
	seen, err := nativeCaptureCursors(input.Seen)
	if err != nil {
		return result, err
	}
	result.Text, err = p.renderNativeFinal(ctx, resource, seen, retained)
	return result, err
}

func (p *Preparer) renderNativeFinal(ctx context.Context, resource nativeFinalResource, seen map[string]uint64, retained *recovery.Session) (string, error) {
	var body strings.Builder
	if len(seen) != 0 {
		body.WriteString("Captures already reported by yield summaries omitted; explicit mchanges reads retain full diffs.\n")
	}
	result := compact.CapturedSource{}
	for _, source := range resource.Sources {
		output := compact.CapturedSource{Command: source.Command}
		var err error
		if source.Capture != nil {
			if p.Changes == nil {
				return "", errors.New("completion capture projector is unavailable")
			}
			output.Stdout, err = p.Changes.ProjectFrozen(*source.Capture, seen)
		} else {
			err = errors.New(source.Error)
		}
		if err != nil {
			output.ExitCode, output.Stderr = 1, "mchanges: "+err.Error()+"\n"
		}
		body.WriteString(output.Text())
		body.WriteByte('\n')
		result.ExitCode = max(result.ExitCode, output.ExitCode)
	}
	result.Stdout = body.String()
	if err := boundCompletion(ctx, &result, retained); err != nil {
		return "", err
	}
	return result.Text(), nil
}

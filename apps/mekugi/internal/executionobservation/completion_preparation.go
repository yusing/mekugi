package executionobservation

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"strings"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
	"github.com/yusing/mekugi/modules/agentguidance/compact"
	"github.com/yusing/mekugi/modules/outputrecovery/recovery"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

// PrepareCompletion runs on the existing input request worker before injection.
// Retain the ordinary reload reader/result with the answer so the next inference
// consumes completed evidence instead of scheduling another acquisition turn.
func (p *Preparer) PrepareCompletion(ctx context.Context, input control.NativeInput) (sessioncontrol.PreparedCompletion, error) {
	var prepared sessioncontrol.PreparedCompletion
	if input.Kind != "FINAL_ANSWER" || input.Outcome != control.Completed {
		return prepared, nil
	}
	_, selection, found := compact.SplitCompactReload(input.Message)
	if !found || compact.CompletionSources(selection) != nil {
		return prepared, nil
	}
	reader, _, err := compactReaderCall(compact.Record{Reference: control.MessageItemID(input.ID), Selection: selection}, nil, input.Thread)
	if err != nil || len(reader) == 0 {
		return prepared, err
	}
	var call map[string]jsontext.Value
	if err := json.Unmarshal(reader, &call); err != nil {
		return prepared, err
	}
	output, code, captureErr := p.completionSnapshot(ctx, input.Thread, selection)
	if captureErr != nil {
		if ctx.Err() != nil {
			return prepared, ctx.Err()
		}
		code = 1
		output = fmt.Sprintf("CONTENT NOT ATTACHED: %v\n", captureErr)
	}
	result := fmt.Sprintf("Script completed\nOutput:\n%s", output)
	prepared.Source = responses.StringField(call, "call_id")
	prepared.Items = []jsontext.Value{reader, responses.MustMarshalJSON(map[string]any{"type": "custom_tool_call_output", "call_id": responses.StringField(call, "call_id"), "output": result})}
	sources, _ := compact.ReloadSources(selection)
	var ids []string
	for _, source := range sources {
		ids = append(ids, source.Changes...)
	}
	prepared.Reload = []activity.ReloadSource{{Command: compact.CaptureCommand(selection), Changes: strings.Join(ids, " "), Preload: true, ExitCode: new(code)}}
	return prepared, nil
}

func (p *Preparer) completionSnapshot(ctx context.Context, thread, selection string) (string, int, error) {
	if p.Retained.Store == nil || p.Recovery == nil || p.ThreadControl == nil {
		return "", 1, errors.New("completion attachment storage is unavailable")
	}
	member, ok := p.ThreadControl.Coordination().Member(thread)
	if !ok {
		return "", 1, control.ErrNotAdmitted
	}
	selected := p.Retained
	selected.Thread = thread
	var err error
	selected.Namespace, err = selected.Store.NamespaceForThread(thread)
	if err != nil {
		return "", 1, err
	}
	store, err := p.Recovery.AttachStore(selected.Store, selected.Namespace, thread, recovery.DefaultLimits(), selected.Writes)
	if err != nil {
		return "", 1, err
	}
	contextID := p.Control.ContextIdentity(thread)
	snapshot, err := ReadCompletion(ctx, selection, member.Settings.Workspace, compact.ChangeReader{Service: p.Changes, Scope: selected, Yielded: p.ThreadControl.Coordination().YieldCursor(thread, contextID), YieldContext: contextID}, store)
	if err != nil {
		return "", 1, err
	}
	data, err := json.Marshal(&snapshot)
	if err != nil {
		return "", 1, err
	}
	ref, err := store.Publish(ctx, command.OmittedOutput{Stdout: string(data)}, 0)
	return compact.SnapshotPrefix + ref + "\n", snapshot.Completion.ExitCode, err
}

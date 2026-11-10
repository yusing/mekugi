package codexbinding

import (
	"context"
	json "encoding/json/v2"
	"strings"

	"github.com/yusing/mekugi/libraries/modelprotocol/attachments"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

func messageSelection(message string) string {
	var sources []string
	seen := make(map[string]bool)
	for ref := range attachments.References(message) {
		if !strings.HasPrefix(ref, "@") {
			continue // Coordination messages admit file attachments, not commands.
		}
		if !seen[ref] {
			sources = append(sources, ref)
			seen[ref] = true
		}
	}
	if len(sources) == 0 {
		return ""
	}
	data, _ := json.Marshal(struct {
		Sources []string `json:"sources"`
	}{sources})
	return string(data)
}

func acquireMessageSources(ctx context.Context, owner control.Runtime, member *control.Member, selection string) (string, error) {
	reader, ok := owner.(control.MessageSourceReader)
	if !ok {
		return "CONTENT NOT ATTACHED: recipient source acquisition is unavailable", nil
	}
	text, err := reader.ReadMessageSources(ctx, member, selection)
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil {
		return "CONTENT NOT ATTACHED: " + err.Error(), nil
	}
	return text, nil
}

func (d *Driver) freezeMessage(ctx context.Context, caller control.Identity, route, message string, target *control.Member, owner control.Runtime) (string, error) {
	selection := messageSelection(message)
	if selection == "" {
		return "", nil
	}
	return d.control.FreezeMessage(ctx, caller, route+":"+target.Thread, message, func() (string, error) {
		return acquireMessageSources(ctx, owner, target, selection)
	})
}

func (d *Driver) prepareNativeInput(action control.Action) (control.NativeInput, error) {
	input := nativeInput(action)
	if action.Effect.Sources == "" || input.Message == "" {
		return input, nil
	}
	message, err := d.control.MessageText(action.Effect.Caller, action.Effect.Sources, action.Effect.Message)
	if err != nil {
		return control.NativeInput{}, err
	}
	input.Message = message
	return input, nil
}

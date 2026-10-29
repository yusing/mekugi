package codexbinding

import (
	"bytes"
	"context"
	"errors"
	"html"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	"github.com/yusing/mekugi/modules/agentguidance/environment"
)

const environmentStart = "<mekugi_environment_facts>"
const environmentEnd = "</mekugi_environment_facts>"

func projectEnvironment(ctx context.Context, input binding.InstructionInput, admitted binding.EnvironmentContext, store *environment.Store) (binding.InstructionUpdate, error) {
	result := binding.InstructionUpdate{InstructionInput: input}
	text, ok := responses.DecodeString(input.Instructions)
	if !ok && len(input.Instructions) != 0 && !bytes.Equal(bytes.TrimSpace(input.Instructions), []byte("null")) {
		return result, errors.New("base instructions must be a string or null")
	}
	facts := store.Snapshot(ctx, environment.Context{Session: admitted.Session, Thread: admitted.Thread, Workspace: admitted.Workspace, Revision: admitted.Revision})
	body := html.EscapeString(facts.Text(admitted.Workspace))
	// Keep snapshots in the owned base layer, outside native conversation history.
	// Refresh replaces inherited copies without changing tool/result boundaries.
	updated, err := replaceSection(text, environmentStart+"\n"+body+"\n"+environmentEnd, environmentStart, environmentEnd, false, false)
	if err != nil {
		return result, err
	}
	if updated != text {
		result.Instructions = responses.MustMarshalJSON(updated)
		result.InstructionsChanged = true
	}
	return result, nil
}

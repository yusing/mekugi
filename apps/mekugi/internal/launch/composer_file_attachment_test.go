package launch

import (
	"bytes"

	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"

	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/modelprotocol/attachments"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"

	jsonv1 "encoding/json"

	"strings"
	"testing"
)

func TestComposerFileAttachmentProjectionLeavesLookalikes(t *testing.T) {
	for _, item := range []any{
		map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "input_text", "text": attachments.Encode([]string{"Attached file data"})}}},
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": "quoted " + attachments.Encode([]string{"Attached file data"})}}},
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": attachments.Prefix + "invalid" + attachments.Suffix}}},
	} {
		raw := responses.MustMarshalJSON([]any{item})
		request := codexresponses.Request{Fields: map[string]jsonv1.RawMessage{"input": raw}}
		requestexecution.ProjectFileAttachments(&request)
		if !bytes.Equal(raw, request.Fields["input"]) {
			t.Fatal("changed non-attachment input")
		}
	}
}

func TestComposerFileAttachmentLeavesOrdinaryUnicodeLimitToCodex(t *testing.T) {
	u, w := newAppServerTestUI()
	d := conversationmodel.Input{Text: strings.Repeat("界", 400_000)}
	if err := sendConversationInput(&u.attached, &u.Client, &u.picker, &u.questions, &u.approvals, u.clock, u.settings.Model, u.serviceTiers, u.skillEnvironment, u.ownedImages, &u.guardHookCheck, []conversationmodel.Input{d}, false); err != nil {
		t.Fatal(err)
	}
	requests := appServerTurnRequests(t, w)
	if len(requests) != 1 || requests[0].text() != d.Text {
		t.Fatal("attachment safety limit rejected an ordinary Unicode prompt")
	}
}

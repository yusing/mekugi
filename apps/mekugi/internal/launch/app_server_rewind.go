package launch

import (
	"cmp"
	"strings"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/backends/codex/binding"
	conversationbinding "github.com/yusing/mekugi/modules/conversation/codexbinding"
	"github.com/yusing/mekugi/modules/conversation/completion"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"
)

func showRewindPicker(control *sessioncontrol.Client[appServerRequest], picker *composerPicker, provider string, approvals bool) {
	picker.cancelScan()
	picker.openMenu("rewind")
	picker.Target, picker.Choices, picker.Problem, picker.Loading = completion.Target{}, nil, "", true
	picker.rewindThread, picker.rewindProvider, picker.rewindApprovals = control.Thread, provider, approvals
	if _, err := control.Requests.Send("thread/read", map[string]any{"threadId": control.Thread, "includeTurns": true}, "main", appServerRequest{label: "rewind/turns", metadata: control.Thread, menu: picker.ID}); err != nil {
		picker.Loading, picker.Problem = false, "Could not load user turns: "+err.Error()
	}
}

func rewindTurnsResponse(control *sessioncontrol.Client[appServerRequest], picker *composerPicker, request appServerRequest, message appserver.Message) {
	if !picker.Open || picker.Kind != "rewind" || picker.ID != request.menu || picker.rewindThread != request.metadata || control.Thread != request.metadata {
		return
	}
	picker.Loading = false
	if message.Error != nil {
		picker.Problem = "Could not load user turns: " + message.Error.Message
		return
	}
	thread, err := binding.DecodeHistoryThread(message.Result, request.metadata)
	if err != nil {
		picker.Problem = "Could not load user turns: " + err.Error()
		return
	}
	for _, turn := range thread.Turns {
		if turn.ID == "" || turn.Status == "inProgress" {
			continue
		}
		for _, item := range turn.Items {
			if item.Type != "userMessage" {
				continue
			}
			text, _, _ := conversationbinding.UserText(item.Content)
			if answer, _, replied := conversationbinding.ReplyText(text); replied {
				text = answer
			}
			text = strings.Join(strings.Fields(text), " ")
			picker.Choices = append(picker.Choices, completion.Choice{Name: turn.ID, Display: cmp.Or(text, "User turn (no text)"), Description: "Fork immediately before this turn"})
			break
		}
	}
	if len(picker.Choices) == 0 {
		picker.Problem = "No user turns available to rewind"
	}
}

func chooseRewind(attached *attachedPresentation, terminal *terminalsession.Attachment, control *sessioncontrol.Client[appServerRequest], picker *composerPicker, applied preferences.Applied, restoring bool, clock func() time.Time, choice completion.Choice) {
	if restoring || control.Replacement.Pending() || control.Thread != picker.rewindThread || sessionBusy(&control.Live, attached.conversation) {
		setNotice(&attached.feedback, terminal, presentationTime(clock), "Wait for the session to be idle before rewinding", true)
		return
	}
	if err := control.Requests.Fork(&control.Startup, &control.Live, applied, picker.rewindProvider, picker.rewindApprovals, choice.Name); err != nil {
		setNotice(&attached.feedback, terminal, presentationTime(clock), "Could not rewind session: "+err.Error(), true)
		return
	}
	picker.Kind, picker.Open, picker.Choices, picker.rewindThread = "", false, nil, ""
	attached.presentation.Status.SetText("Forking before selected turn…")
}

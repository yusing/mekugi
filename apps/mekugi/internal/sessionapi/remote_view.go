package sessionapi

import (
	json "encoding/json/v2"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/modules/conversation/composer"
)

// RemoteMutation is the public semantic input shape. Answers use the public
// question contract; native preparation and final-batch fields stay private.
type RemoteMutation struct {
	Attachment string                      `json:"attachment"`
	Version    sessionpresentation.Version `json:"version"`
	Operation  RemoteOperation             `json:"operation"`
}

type RemoteOperation struct {
	NativeOperation
	Answers *Answer `json:"answers,omitempty"`
}

// remoteMutation converts the public wire value at the authority boundary.
// The pending prompt owner resolves choices; transport cannot infer state.
type remoteMutation struct{ NativeMutation }

func (m *remoteMutation) UnmarshalJSON(data []byte) error {
	var remote RemoteMutation
	if err := json.Unmarshal(data, &remote, json.WithUnmarshalers(json.UnmarshalFunc(func(data []byte, answer *Answer) error {
		return json.Unmarshal(data, answer, json.RejectUnknownMembers(true))
	}))); err != nil {
		return err
	}
	m.NativeMutation = NativeMutation{Attachment: remote.Attachment, Version: remote.Version, Operation: remote.Operation.NativeOperation}
	if remote.Operation.Answers != nil {
		m.Operation.Answers = &NativeAnswers{Call: remote.Operation.Answers.Call, public: remote.Operation.Answers}
	}
	return nil
}

func textOnlyDraft(d composer.Draft) bool {
	return len(d.Images) == 0 && len(d.Files) == 0 && len(d.Skills) == 0 && len(d.Selections) == 0 &&
		len(d.AnswerImages) == 0 && len(d.Attachments) == 0 && d.AttachmentNotice == "" && !d.InHistory
}

func (o NativeOperation) routeRefusal(authority viewAuthority) string {
	if o.Recovery != nil && o.Recovery.Action != RetryRecovery && o.Recovery.Action != DiscardRecovery && o.Recovery.Action != EditRecovery {
		return "Choose retry, edit or discard for retained input."
	}
	if o.Recovery != nil && o.Recovery.Text != nil && o.Recovery.Action != RetryRecovery {
		return "Edited text is accepted only when sending retained input."
	}
	if authority == localNativeView {
		if o.Composer != nil && o.Composer.Input != nil {
			return "Native composer operations use Draft, not public input descriptors."
		}
		return ""
	}
	if o.Preferences != nil || o.SaveDraft != nil || o.Composer != nil && o.Composer.Intent.Operation.ClientLocal() {
		return "This operation belongs to the native client."
	}
	for _, editor := range []*NativeEditorInput{o.Editor, o.Completion} {
		if editor != nil && !textOnlyDraft(editor.Draft) {
			return "Remote editor metadata accepts only text and caret."
		}
	}
	if c := o.Composer; c != nil {
		if c.Draft.Text != "" || c.Draft.CursorBack != 0 || !textOnlyDraft(c.Draft) {
			return "Remote composer input requires public input descriptors."
		}
		submits := c.Intent.Operation == sessionpresentation.SubmitComposer || c.Intent.Operation == sessionpresentation.QueueComposer
		if submits != (c.Input != nil) {
			return "Only Submit and Queue require public input descriptors."
		}
	}
	return ""
}

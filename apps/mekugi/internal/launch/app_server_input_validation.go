package launch

import (
	"errors"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	conversationbinding "github.com/yusing/mekugi/modules/conversation/codexbinding"
	"github.com/yusing/mekugi/modules/conversation/composer"
)

// prepareSubmissionInput applies the authoritative host text budget after
// assigning the real submission identity and mapping all text parts. API
// preparation uses a detached clone; final submission retains its own identity.
func prepareSubmissionInput(draft *composer.Draft, id string) ([]map[string]any, error) {
	if err := draft.IdentifyAttachments(id); err != nil {
		return nil, err
	}
	input := conversationbinding.Input(*draft)
	textBytes := 0
	for _, part := range input {
		text, _ := part["text"].(string)
		textBytes += len(text)
	}
	if (len(draft.Attachments) > 0 || len(draft.Skills) > 0) && textBytes > codexresponses.UserTextLimit {
		return nil, errors.New("Input with attachments exceeds the safe 1 MiB text limit. Reduce the draft or attachments and retry.")
	}
	return input, nil
}

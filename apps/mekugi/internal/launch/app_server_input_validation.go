package launch

import (
	"errors"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	conversationbinding "github.com/yusing/mekugi/modules/conversation/codexbinding"
	"github.com/yusing/mekugi/modules/conversation/composer"
)

// prepareSubmissionInput applies the authoritative host text budget after
// mapping all text parts, including ordinary Markdown for supplied selections.
func prepareSubmissionInput(draft *composer.Draft) ([]map[string]any, error) {
	input := conversationbinding.Input(*draft)
	textBytes := 0
	for _, part := range input {
		text, _ := part["text"].(string)
		textBytes += len(text)
	}
	if (len(draft.Selections) > 0 || len(draft.Files) > 0 || len(draft.Skills) > 0 || draft.AnswerSelections != "") && textBytes > codexresponses.UserTextLimit {
		return nil, errors.New("Input with attachments exceeds the safe 1 MiB text limit. Reduce the draft or attachments and retry.")
	}
	return input, nil
}

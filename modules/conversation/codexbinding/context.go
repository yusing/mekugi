package codexbinding

import (
	"crypto/sha256"
	json "encoding/json/v2"
	"fmt"
	"slices"
	"strings"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/libraries/modelprotocol/attachments"
	"github.com/yusing/mekugi/modules/conversation/composer"
)

// CompactedAttachments selects submitted attachments preceding the most recent
// completed boundary. Native history supplies the exact fork/rewind prefix and
// text-element placeholders; ordinary prompt text never becomes reload context.
func CompactedAttachments(turns []binding.HistoryTurn) []composer.ContextAttachment {
	var result []composer.ContextAttachment
	boundary := 0
	for _, turn := range turns {
		for _, item := range turn.Items {
			if item.Type == "contextCompaction" && (item.Status == "" || item.Status == "completed") {
				boundary = len(result)
			}
			if item.Type != "userMessage" {
				continue
			}
			var parts []UserContent
			if json.Unmarshal(item.Content, &parts) != nil {
				continue
			}
			var placeholders []string
			var selectionLabels []string
			var suppliedText string
			var images []composer.Token
			for i, source := range userImages(parts) {
				if strings.HasPrefix(source, "data:") {
					media, _, _ := strings.Cut(source, ";")
					media, _, _ = strings.Cut(media, ",")
					source = media + " (embedded image; pixels not retained)"
				}
				images = append(images, composer.Token{Name: fmt.Sprintf("[Image %d]", i+1), Path: source})
			}
			for _, part := range parts {
				if part.Type != "text" {
					continue
				}
				for _, span := range ElementSpans(part.Text, part.Elements) {
					label := part.Text[span.Start:span.End]
					placeholders = append(placeholders, label)
					if span.Kind == composer.Selection || strings.HasPrefix(label, "[Uploaded ") {
						selectionLabels = append(selectionLabels, label)
					}
				}
			}
			if len(parts) > 1 {
				last := parts[len(parts)-1]
				if last.Type == "text" && strings.HasPrefix(last.Text, composer.UserSelectionsPrefix) {
					suppliedText = strings.Join(selectionLabels, "\n") + last.Text
				}
			}
			for _, part := range parts {
				if part.Type != "text" {
					continue
				}
				snapshot, ok := attachments.DecodeSnapshot(part.Text)
				if !ok {
					continue
				}
				value := composer.ContextAttachment{ID: snapshot.ID, Original: sha256.Sum256([]byte(part.Text)), Placeholders: placeholders, Images: images, Selection: attachments.Snapshot{ID: snapshot.ID}}
				for _, frame := range snapshot.Frames {
					if attachments.SelectionFrame(frame) {
						value.Selection.Frames = append(value.Selection.Frames, frame)
					}
					if name, _, _ := attachments.ParseSkillFrame(frame); name != "" && !slices.Contains(value.Placeholders, "$"+name) {
						value.Placeholders = append(value.Placeholders, "$"+name)
					}
				}
				result = append(result, value)
				placeholders = nil
				images = nil
			}
			if len(images) > 0 || len(placeholders) > 0 || suppliedText != "" {
				result = append(result, composer.ContextAttachment{Images: images, Placeholders: placeholders, SelectionText: suppliedText})
			}
		}
	}
	return result[:boundary]
}

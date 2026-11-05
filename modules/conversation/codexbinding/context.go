package codexbinding

import (
	"crypto/sha256"
	json "encoding/json/v2"
	"fmt"
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
					placeholders = append(placeholders, part.Text[span.Start:span.End])
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
				seen := make(map[string]bool)
				for _, frame := range snapshot.Frames {
					if attachments.SelectionFrame(frame) {
						value.Selection.Frames = append(value.Selection.Frames, frame)
					}
					if name, path, _ := attachments.ParseSkillFrame(frame); name != "" && !seen[name+"\x00"+path] {
						seen[name+"\x00"+path] = true
						value.Skills = append(value.Skills, composer.Token{Name: name, Path: path})
					}
				}
				result = append(result, value)
				placeholders = nil
				images = nil
			}
			if len(images) > 0 {
				result = append(result, composer.ContextAttachment{Images: images})
			}
		}
	}
	return result[:boundary]
}

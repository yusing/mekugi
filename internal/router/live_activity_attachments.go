package router

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"strconv"
	"strings"

	"github.com/yusing/mekugi/internal/livediff"
	"github.com/yusing/mekugi/internal/pathdisplay"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
)

// Derive receipts from the submitted envelope, never from current filesystem
// state or file-body text. The same host user item supplies live and replay UI.
func appServerAttachmentBlocks(cwd string, content jsontext.Value) []activityui.Block {
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(content, &parts) != nil {
		return nil
	}
	var blocks []activityui.Block
	seen := make(map[string]bool)
	for _, part := range parts {
		if part.Type != "text" {
			continue
		}
		frames, ok := decodeFileAttachments(part.Text)
		if !ok {
			continue
		}
		for _, frame := range frames {
			if name, _, rest := skillAttachmentFrame(frame); name != "" {
				verb, label := "Attached skill", name
				if strings.HasPrefix(rest, ": CONTENT NOT ATTACHED (") {
					reason, err := strconv.QuotedPrefix(strings.TrimPrefix(rest, ": CONTENT NOT ATTACHED ("))
					if err != nil {
						continue
					}
					message, _ := strconv.Unquote(reason)
					verb, label = "Attach failed", "skill "+name+" · "+message
				}
				key := verb + "\x00skill\x00" + name
				if !seen[key] {
					seen[key] = true
					blocks = append(blocks, activityui.Block{Kind: "op", Verb: verb, Label: livediff.Safe(label, false)})
				}
				continue
			}
			verb, label, path := "Attached", "", ""
			rest := strings.TrimPrefix(frame, "Attached file ")
			quoted, err := strconv.QuotedPrefix(rest)
			if err == nil {
				rest = rest[len(quoted):]
				path, _ = strconv.Unquote(quoted)
				switch {
				case strings.HasPrefix(rest, " (UTF-8 bytes "):
					// Multiple content chunks represent one attached file.
				case strings.HasPrefix(rest, ": CONTENT NOT ATTACHED ("):
					reason, err := strconv.QuotedPrefix(strings.TrimPrefix(rest, ": CONTENT NOT ATTACHED ("))
					if err != nil {
						continue
					}
					message, _ := strconv.Unquote(reason)
					verb, label = "Attach failed", message
				default:
					continue
				}
			} else if frame == "Attached file references: remaining contents NOT ATTACHED (attachment budget exceeded). Read remaining referenced files separately if needed." {
				verb, label = "Attach failed", "remaining files · attachment budget exceeded"
			} else {
				continue
			}
			key := verb + "\x00" + path + "\x00" + label
			if !seen[key] {
				seen[key] = true
				blocks = append(blocks, activityui.Block{Kind: "op", Verb: verb, Path: pathdisplay.ForWorkspace(cwd, path), Label: livediff.Safe(label, false)})
			}
		}
	}
	return blocks
}

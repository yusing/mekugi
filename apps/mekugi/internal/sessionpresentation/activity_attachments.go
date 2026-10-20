package sessionpresentation

import (
	"encoding/json/jsontext"

	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"

	"github.com/yusing/mekugi/libraries/textsanitize"

	activitymodel "github.com/yusing/mekugi/modules/activity/feed"

	json "encoding/json/v2"
	"strconv"
	"strings"

	"github.com/yusing/mekugi/libraries/modelprotocol/attachments"

	"github.com/yusing/mekugi/libraries/pathdisplay"
)

// Source: apps/mekugi/internal/router/live_activity_attachments.go:1:120@45fbe279

// Derive receipts from the submitted envelope, never from current filesystem
// state or file-body text. The same host user item supplies live and replay UI.
func AttachmentValues(cwd string, content jsontext.Value) []activitypresentation.Value {
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(content, &parts) != nil {
		return nil
	}
	var blocks []activitypresentation.Value
	seen := make(map[string]bool)
	contents := make(map[int]string)
	indexes := make(map[string]int)
	appendContent := func(key, rest string, block activitypresentation.Value, source string) {
		_, body, ok := strings.Cut(rest, "):\n")
		if !ok {
			return
		}
		index, exists := indexes[key]
		if !exists {
			index = len(blocks)
			indexes[key] = index
			block.Reads = []activitymodel.Read{{Path: source}}
			blocks = append(blocks, block)
		}
		contents[index] += body
	}
	for _, part := range parts {
		if part.Type != "text" {
			continue
		}
		snapshot, ok := attachments.DecodeSnapshot(part.Text)
		if !ok {
			continue
		}
		// Each envelope is a submitted snapshot; only its chunks belong together.
		clear(indexes)
		for _, frame := range snapshot.Frames {
			if name, source, rest := attachments.ParseSkillFrame(frame); name != "" {
				verb, label := "Attached skill", name
				if strings.HasPrefix(rest, ": CONTENT NOT ATTACHED (") {
					reason, err := strconv.QuotedPrefix(strings.TrimPrefix(rest, ": CONTENT NOT ATTACHED ("))
					if err != nil {
						continue
					}
					message, _ := strconv.Unquote(reason)
					verb, label = "Attach failed", "skill "+name+" · "+message
				}
				key := verb + "\x00skill\x00" + name + "\x00" + source
				if verb == "Attached skill" {
					appendContent(key, rest, activitypresentation.Value{Kind: "op", Verb: verb, Label: textsanitize.Safe(label, false), AttachmentID: snapshot.ReceiptID(frame)}, source)
					continue
				}
				if !seen[key] {
					seen[key] = true
					blocks = append(blocks, activitypresentation.Value{Kind: "op", Verb: verb, Label: textsanitize.Safe(label, false)})
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
			if verb == "Attached" {
				appendContent(key, rest, activitypresentation.Value{Kind: "op", Verb: verb, Path: pathdisplay.ForWorkspace(cwd, path), AttachmentID: snapshot.ReceiptID(frame)}, path)
				continue
			}
			if !seen[key] {
				seen[key] = true
				blocks = append(blocks, activitypresentation.Value{Kind: "op", Verb: verb, Path: pathdisplay.ForWorkspace(cwd, path), Label: textsanitize.Safe(label, false)})
			}
		}
	}
	for index, content := range contents {
		blocks[index].Tail = strings.Split(textsanitize.Safe(content, false), "\n")
	}
	return blocks
}

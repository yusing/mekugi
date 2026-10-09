package router

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/yusing/mekugi/internal/commentaryid"
	responseevents "github.com/yusing/mekugi/internal/responses"
)

func qualifiedToolName(namespace, name string) string {
	if namespace == "" {
		return name
	}
	return namespace + "." + name
}

func commentaryMessageID(seed string) string {
	digest := sha256.Sum256([]byte(seed))
	return fmt.Sprintf("%s%x", commentaryid.OperationPrefix, digest[:12])
}

// assistantCommentaryMessage creates an assistant commentary message with the given ID and text.
func assistantCommentaryMessage(id, text string) map[string]json.RawMessage {
	type outputText struct {
		Annotations []any  `json:"annotations"`
		Text        string `json:"text"`
		Type        string `json:"type"`
	}
	type outputMessage struct {
		Type    string       `json:"type"`
		ID      string       `json:"id,omitempty"`
		Role    string       `json:"role"`
		Status  string       `json:"status"`
		Phase   string       `json:"phase"`
		Content []outputText `json:"content"`
	}
	encoded := mustMarshalJSON(outputMessage{
		Role:   "assistant",
		Type:   "message",
		ID:     id,
		Status: "completed",
		Phase:  "commentary",
		Content: []outputText{{
			Type:        "output_text",
			Text:        text,
			Annotations: []any{},
		}},
	})
	var message map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &message); err != nil {
		panic(err)
	}
	return message
}

// assistantCommentaryDoneEvent creates a response.output_item.done event for a commentary message.
func assistantCommentaryDoneEvent(message map[string]json.RawMessage) []byte {
	return mustMarshalJSON(struct {
		Type string                     `json:"type"`
		Item map[string]json.RawMessage `json:"item"`
	}{
		Type: responseevents.OutputItemDone,
		Item: message,
	})
}

func (p *mekugiProxy) commentaryMessageIDs(sessionID string) map[string]struct{} {
	p.mu.RLock()
	defer p.mu.RUnlock()
	result := make(map[string]struct{})
	for id := range p.memoryCommentary[sessionID] {
		result[id] = struct{}{}
	}
	if session := p.sessions[sessionID]; session != nil {
		for _, history := range session.calls {
			for _, messageID := range history.CommentaryMessageIDs {
				result[messageID] = struct{}{}
			}
		}
	}
	return result
}

// commentaryCode keeps backticks in names or previews from ending the code span.
func commentaryCode(value string) string {
	longest, run := 0, 0
	for _, r := range value {
		if r == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	fence := strings.Repeat("`", longest+1)
	if longest > 0 || strings.HasPrefix(value, " ") || strings.HasSuffix(value, " ") {
		return fence + " " + value + " " + fence
	}
	return fence + value + fence
}

// retainCommentary is called only at router-authored message construction sites.
// A provider's use of a reserved-looking ID is not proof of router provenance.
func (t *mekugiResponseTransform) retainCommentary(messages ...map[string]json.RawMessage) []map[string]json.RawMessage {
	return t.retainCommentaryReplacing(nil, messages...)
}

func (t *mekugiResponseTransform) retainCommentaryReplacing(replacement *commentaryReplacement, messages ...map[string]json.RawMessage) []map[string]json.RawMessage {
	if t.proxy.replayStore == nil {
		t.proxy.mu.Lock()
		defer t.proxy.mu.Unlock()
		if t.proxy.memoryCommentary == nil {
			t.proxy.memoryCommentary = make(map[string]map[string]*commentaryReplacement)
		}
		retained := t.proxy.memoryCommentary[t.historySessionID]
		newIDs := make(map[string]*commentaryReplacement)
		for _, message := range messages {
			id := jsonString(message, "id")
			previous, exists := retained[id]
			if previous != nil && replacement != nil && (previous.Thread != replacement.Thread || previous.Turn != replacement.Turn || !slices.Equal(previous.Items, replacement.Items)) {
				return nil
			}
			if id != "" && (!exists || previous == nil && replacement != nil) {
				newIDs[id] = replacement
			}
		}
		if retained == nil {
			retained = make(map[string]*commentaryReplacement)
			t.proxy.memoryCommentary[t.historySessionID] = retained
		}
		maps.Copy(retained, newIDs)
		return messages
	}
	var ids []string
	for _, message := range messages {
		if id := jsonString(message, "id"); id != "" {
			ids = append(ids, id)
		}
	}
	if len(ids) != 0 {
		if err := t.proxy.replayStore.putCommentaryReplacing(t.ctx, t.directory, ids, replacement); err != nil {
			t.proxy.notice(t.sessionID, t.shellThreadID, "commentary_storage", "Mekugi could not retain progress-message provenance. New auxiliary progress messages were suppressed; tool execution and provider answers are unchanged. Check session storage space and permissions.")
			return nil
		}
	}
	return messages
}

// Missing provenance keeps provider content. Neither a reserved-looking ID nor
// identical text proves that a child result contains another host message.
func (p *mekugiProxy) commentaryReplacementItems(ctx context.Context, workspace, thread, turn, item string) []string {
	// Generated child results use this prefix. It only avoids a lookup for
	// ordinary provider IDs; the retained record still proves replacement.
	if p == nil || thread == "" || turn == "" || !strings.HasPrefix(item, commentaryid.OperationPrefix) {
		return nil
	}
	var replacement *commentaryReplacement
	if p.replayStore == nil {
		p.mu.RLock()
		replacement = p.memoryCommentary[workspace+"\x00"+thread][item]
		p.mu.RUnlock()
	} else {
		err := p.replayStore.locked(ctx, func() error {
			record, _, err := p.replayStore.read(workspace, item, true)
			replacement = record.Replacement
			return err
		})
		if err != nil {
			return nil
		}
	}
	if replacement == nil || replacement.Thread != thread || replacement.Turn != turn {
		return nil
	}
	return slices.Clone(replacement.Items)
}

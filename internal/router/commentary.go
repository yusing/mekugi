package router

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/openai/openai-go/v3/responses"
	"github.com/yusing/mekugi/internal/commentaryid"
)

const commentaryArgumentName = "journal"

type commentaryTool struct {
	qualifiedName string
}

type commentaryToolCatalog map[string]commentaryTool

func functionToolKey(namespace, name string) string {
	return namespace + "\x00" + name
}

func prepareCommentaryTools(fields map[string]json.RawMessage, tools *responsesToolCatalog) (commentaryToolCatalog, error) {
	catalog := make(commentaryToolCatalog)
	seen := make(map[string]struct{})
	instrument := func(namespace string, tool *responsesToolDefinition, addParameter bool) error {
		if tool.Type != "function" {
			return nil
		}
		name := tool.Name
		if name == "" || commentaryExcluded(namespace, name) {
			return nil
		}
		key := functionToolKey(namespace, name)
		qualifiedName := qualifiedToolName(namespace, name)
		if _, exists := seen[key]; exists {
			return fmt.Errorf("commentary tool %q is defined more than once", qualifiedName)
		}
		seen[key] = struct{}{}
		var strict bool
		_ = json.Unmarshal(tool.rawField("strict"), &strict)
		if addParameter && !strict {
			var parameters map[string]json.RawMessage
			if json.Unmarshal(tool.rawField("parameters"), &parameters) == nil && jsonString(parameters, "type") == "object" {
				var properties map[string]json.RawMessage
				if raw, exists := parameters["properties"]; !exists {
					properties = make(map[string]json.RawMessage)
				} else if json.Unmarshal(raw, &properties) != nil || properties == nil {
					return fmt.Errorf("%s parameters properties must be an object", qualifiedName)
				}
				if _, owned := properties[commentaryArgumentName]; !owned {
					properties[commentaryArgumentName] = journalMutationsSchema()
					parameters["properties"] = mustMarshalJSON(properties)
					tool.setRawField("parameters", mustMarshalJSON(parameters))
					catalog[key] = commentaryTool{qualifiedName: qualifiedName}
				}
			}
		}
		return nil
	}

	if tools.top.present {
		if err := tools.top.err; err != nil {
			return nil, fmt.Errorf("decode Responses tools for commentary: %w", err)
		}
		for _, tool := range tools.top.tools {
			if err := instrument("", tool, true); err != nil {
				return nil, err
			}
		}
		fields["tools"] = mustMarshalJSON(tools.top.tools)
	}

	if tools.inputObjectsErr != nil {
		return catalog, nil
	}
	// The provider owns configured additional_tools schemas; the router never
	// adds a commentary parameter to them.
	for _, group := range tools.additional {
		if !group.tools.present {
			return nil, errors.New("decode additional tools for commentary: unexpected end of JSON input")
		}
		if err := group.tools.err; err != nil {
			return nil, fmt.Errorf("decode additional tools for commentary: %w", err)
		}
		for index, tool := range group.tools.tools {
			if tool.Type != "namespace" {
				if err := instrument("", tool, false); err != nil {
					return nil, err
				}
				continue
			}
			namespace := tool.Name
			node := group.tools.nodes[index]
			if node == nil || node.nested == nil {
				return nil, fmt.Errorf("decode %s tools for commentary: unexpected end of JSON input", namespace)
			}
			if err := node.nested.err; err != nil {
				return nil, fmt.Errorf("decode %s tools for commentary: %w", namespace, err)
			}
			for _, child := range node.nested.tools {
				if err := instrument(namespace, child, false); err != nil {
					return nil, err
				}
			}
		}
	}
	return catalog, nil
}

func qualifiedToolName(namespace, name string) string {
	if namespace == "" {
		return name
	}
	return namespace + "." + name
}

func commentaryExcluded(namespace, name string) bool {
	if namespace == "collaboration" || namespace != "" && slices.Contains([]string{"spawn_agent", "followup_task", "send_message", "wait_agent", "interrupt_agent"}, name) {
		return true
	}
	base := strings.TrimPrefix(name, "functions.")
	if slices.Contains([]string{
		"request_user_input", "request_user_input_async", "send_user_message_async",
		"thread_spawn", "thread_send_input", "thread_resume", "thread_wait", "thread_interrupt",
		"spawn_agent", "followup_task", "send_message", "wait_agent", "interrupt_agent",
	}, base) {
		return true
	}
	qualified := qualifiedToolName(namespace, name)
	return qualified == "functions.send_user_message_async" || qualified == "send_user_message_async"
}

type structuredCommentary struct {
	mutations         []journalMutation
	text              string
	originalArguments string
	arguments         string
}

func extractStructuredCommentary(item map[string]json.RawMessage, catalog commentaryToolCatalog) (structuredCommentary, bool, error) {
	if jsonString(item, "type") != "function_call" {
		return structuredCommentary{}, false, nil
	}
	tool, exists := catalog[functionToolKey(jsonString(item, "namespace"), jsonString(item, "name"))]
	if !exists {
		return structuredCommentary{}, false, nil
	}
	original := jsonString(item, "arguments")
	var arguments map[string]json.RawMessage
	if err := json.Unmarshal([]byte(original), &arguments); err != nil || arguments == nil {
		return structuredCommentary{}, false, errors.New("commentary function arguments must be a JSON object")
	}
	result := structuredCommentary{originalArguments: original, arguments: original}
	if raw, present := arguments[commentaryArgumentName]; present {
		mutations, err := decodeJournalMutations(raw)
		if err != nil {
			return structuredCommentary{}, false, fmt.Errorf("%s: %w", tool.qualifiedName, err)
		}
		result.mutations = mutations
		delete(arguments, commentaryArgumentName)
		result.arguments = string(mustMarshalJSON(arguments))
	}
	return result, true, nil
}

func commentaryMessageID(seed string) string {
	digest := sha256.Sum256([]byte(seed))
	return fmt.Sprintf("%s%x", commentaryid.OperationPrefix, digest[:12])
}

// assistantCommentaryMessage creates an assistant commentary message with the given ID and text.
func assistantCommentaryMessage(id, text string) map[string]json.RawMessage {
	encoded := mustMarshalJSON(responses.ResponseOutputMessageParam{
		ID: id,
		Content: []responses.ResponseOutputMessageContentUnionParam{{
			OfOutputText: new(responses.ResponseOutputTextParam{
				Annotations: []responses.ResponseOutputTextAnnotationUnionParam{},
				Text:        text,
			}),
		}},
		Status: responses.ResponseOutputMessageStatusCompleted,
		Phase:  responses.ResponseOutputMessagePhaseCommentary,
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
		Type: "response.output_item.done",
		Item: message,
	})
}

func (t *mekugiResponseTransform) transformStructuredCommentary(item map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	extracted, matched, err := extractStructuredCommentary(item, t.commentaryTools)
	if err != nil || !matched {
		return nil, err
	}
	callID := jsonString(item, "call_id")
	if callID == "" {
		return nil, errors.New("upstream emitted journal function call without a call ID")
	}
	if retained, exists := t.local[callID]; exists {
		if retained.script != extracted.originalArguments || retained.carrierPayload != extracted.arguments {
			return nil, fmt.Errorf("journal call %q changed arguments", callID)
		}
		item["arguments"] = mustMarshalJSON(extracted.arguments)
		return nil, nil
	}
	var ids []string
	if len(extracted.mutations) != 0 {
		ids, err = t.proxy.journals.apply(t.ctx, t.proxy.replayStore, t.directory, t.shellThreadID, callID+":journal", bindJournalAnswers(extracted.mutations, t.journalQuestion))
		if err != nil {
			return nil, err
		}
		t.featureTrace.record("journal", "tool_field", "mutation", "accepted", callID, "")
	}
	t.recordLocal(callID, &mekugiHistory{
		toolName: qualifiedToolName(jsonString(item, "namespace"), jsonString(item, "name")),
		script:   extracted.originalArguments, carrierKind: codeModeCarrierFunction,
		carrierName: jsonString(item, "name"), carrierPayload: extracted.arguments,
		upstreamItem: maps.Clone(item), journalIDs: ids,
	})
	item["arguments"] = mustMarshalJSON(extracted.arguments)
	return nil, nil
}

func (p *mekugiProxy) drainCommentarySession(sessionID, threadID string) []publishedCommentary {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.commentary == nil {
		return nil
	}
	// Call-scoped deferred progress still needs a non-concurrent session.
	// Shell progress already has exact thread identity and is drained atomically.
	if p.activeSessions[sessionID] > 1 {
		return p.commentary.drainThreadSession(sessionID, threadID)
	}
	return p.commentary.drainSession(sessionID, threadID)
}

// Only thread routes lack a carrier subscription. Keep call-scoped live delivery separate.
func (p *mekugiProxy) drainThreadCommentarySession(sessionID, threadID string) []publishedCommentary {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.commentary == nil {
		return nil
	}
	return p.commentary.drainThreadSession(sessionID, threadID)
}

type commentarySubscription struct {
	token     string
	callID    string
	handedOff bool
}

func (t *mekugiResponseTransform) handOffCommentary(callID string) {
	for index := range t.commentarySubscriptions {
		if t.commentarySubscriptions[index].callID == callID {
			t.commentarySubscriptions[index].handedOff = true
		}
	}
}

func (t *mekugiResponseTransform) releaseCommentarySubscriptions() {
	for _, subscription := range t.commentarySubscriptions {
		// Once the carrier is handed off, publication completion and broker
		// expiry own the route, regardless of how the provider response ends.
		if !subscription.handedOff {
			t.proxy.commentary.cancel(subscription.token)
		}
	}
	t.commentarySubscriptions = nil
}

// validateMekugiCompactionRequest recognizes local Codex compaction requests,
// which stream through /responses without exposing model tools.
// Source: openai/codex codex-rs/core/src/compact.rs:228:273 and client.rs:795:881.

func (p *mekugiProxy) commentaryMessageIDs(sessionID string) map[string]struct{} {
	p.mu.RLock()
	defer p.mu.RUnlock()
	result := make(map[string]struct{})
	maps.Copy(result, p.memoryCommentary[sessionID])
	if p.commentary != nil {
		maps.Copy(result, p.commentary.threadMessageIDs(sessionID))
	}
	if session := p.sessions[sessionID]; session != nil {
		for _, history := range session.calls {
			for _, messageID := range history.commentaryMessageIDs {
				result[messageID] = struct{}{}
			}
		}
	}
	return result
}

func (p *mekugiProxy) addCommentaryMessageID(sessionID, threadID, callID, messageID string) bool {
	if callID == "" {
		return p.commentary.hasThreadMessageID(threadID, messageID)
	}
	history, exists := p.history(sessionID, callID)
	if !exists {
		return false
	}
	if slices.Contains(history.commentaryMessageIDs, messageID) {
		return true
	}
	history.commentaryMessageIDs = append(history.commentaryMessageIDs, messageID)
	return p.rememberBatch(sessionID, map[string]mekugiHistory{callID: history}) == nil
}

func (t *mekugiResponseTransform) runtimeCommentaryMessage(publication publishedCommentary) (message map[string]json.RawMessage) {
	if publication.text == "" {
		return nil
	}
	defer func() {
		source := "shell"
		if publication.callID != "" {
			source = "code_mode"
		}
		outcome := "suppressed"
		if message != nil {
			outcome = "prepared"
		}
		t.featureTrace.record("commentary", source, "render", outcome, publication.callID, publication.messageID)
	}()

	if t.proxy.replayStore != nil && publication.callID != "" {
		// A completed call may have left the bounded memory cache while its
		// authenticated progress subscription is still alive.
		_, found, err := t.proxy.replayStore.lookup(t.ctx, t.directory, publication.callID)
		if err != nil || !found {
			return nil
		}
	} else if !t.proxy.addCommentaryMessageID(t.historySessionID, t.shellThreadID, publication.callID, publication.messageID) {
		return nil
	}

	if history, exists := t.local[publication.callID]; exists && !slices.Contains(history.commentaryMessageIDs, publication.messageID) {
		history.commentaryMessageIDs = append(history.commentaryMessageIDs, publication.messageID)
		t.local[publication.callID] = history
	}
	message = assistantCommentaryMessage(publication.messageID, publication.text)
	if len(t.retainCommentary(message)) == 0 {
		return nil
	}
	return message
}

func attributedCommentary(author, text string) string {
	if author == "" {
		return text
	}
	prefix := "[" + commentaryCode(author) + "] "
	if hasCommentaryAuthor(text, author) {
		return text
	}
	return prefix + text
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

func hasCommentaryAuthor(text, author string) bool {
	return strings.HasPrefix(text, "["+commentaryCode(author)+"] ")
}

func (t *mekugiResponseTransform) operationCommentaryMessage(id, text string) map[string]json.RawMessage {
	if text == "" {
		return nil
	}

	t.proxy.activity.collect(t.threadID, id, "operation", text)
	message := assistantCommentaryMessage(id, attributedCommentary(t.commentaryAuthor, text))
	if len(t.retainCommentary(message)) == 0 {
		return nil
	}
	return message
}

// Completed provider commentary is copied to the root without rewriting the
// child's original message. Router-owned messages already have their own paths.
func (t *mekugiResponseTransform) collectProviderCommentary(message map[string]json.RawMessage) {
	if jsonString(message, "type") != "message" ||
		jsonString(message, "role") != "assistant" || jsonString(message, "phase") != "commentary" ||
		jsonString(message, "status") != "completed" {
		return
	}
	id := jsonString(message, "id")
	t.featureTrace.record("commentary", "provider_message", "authored", "observed", "", id)
	if !t.subagentTurn {
		return
	}
	if id == "" || len(id) > maxCommentaryPublicationBytes-len("provider-message\x00") || commentaryid.Generated(id) {
		return
	}
	var content []map[string]json.RawMessage
	if json.Unmarshal(message["content"], &content) != nil {
		return
	}
	var text strings.Builder
	for _, part := range content {
		if jsonString(part, "type") != "output_text" {
			continue
		}
		value := jsonString(part, "text")
		if len(value) > maxCommentaryPublicationBytes-text.Len() {
			return
		}
		text.WriteString(value)
	}
	t.proxy.activity.collect(t.threadID, "provider-message\x00"+id, "commentary", text.String())
}

// retainCommentary is called only at router-authored message construction sites.
// A provider's use of a reserved-looking ID is not proof of router provenance.
func (t *mekugiResponseTransform) retainCommentary(messages ...map[string]json.RawMessage) []map[string]json.RawMessage {
	if t.proxy.replayStore == nil {
		t.proxy.mu.Lock()
		defer t.proxy.mu.Unlock()
		if t.proxy.memoryCommentary == nil {
			t.proxy.memoryCommentary = make(map[string]map[string]struct{})
		}
		retained := t.proxy.memoryCommentary[t.historySessionID]
		newIDs := make(map[string]struct{})
		for _, message := range messages {
			id := jsonString(message, "id")
			if _, exists := retained[id]; id != "" && !exists {
				newIDs[id] = struct{}{}
			}
		}
		count := 0
		for _, ids := range t.proxy.memoryCommentary {
			count += len(ids)
		}
		if count+len(newIDs) > maxThreadCommentaryIDs {
			return nil
		}
		if retained == nil {
			retained = make(map[string]struct{})
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
	if len(ids) != 0 && t.proxy.replayStore.putCommentary(t.ctx, t.directory, ids) != nil {
		return nil
	}
	return messages
}

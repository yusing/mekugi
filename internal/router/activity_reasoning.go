package router

// Only provider-visible reasoning summaries are presented. Encrypted reasoning
// and raw reasoning content are never decoded or copied into the pane.
func (t *mekugiResponseTransform) collectReasoningDelta(id, delta string) {
	if t.threadID == "" || id == "" || delta == "" || len(id)+len(delta) > maxCommentaryPublicationBytes-t.activityReasoningBytes {
		return
	}
	if t.activityReasoning == nil {
		t.activityReasoning = make(map[string]string)
	}
	if _, exists := t.activityReasoning[id]; !exists {
		t.activityReasoningBytes += len(id)
	}
	t.activityReasoning[id] += delta
	t.activityReasoningBytes += len(delta)
	t.proxy.activity.collectEvent(activityEvent{thread: t.threadID, source: "reasoning\x00" + id, kind: "reasoning", callID: id, text: t.activityReasoning[id]})
}

package router

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/yusing/mekugi/capturer"
)

// Invocation metadata trails the body so host command previews show the program first.
// It is removed before header parsing and never enters the program environment.
const shellInvocationPrefix = "# mekugi:invocation="

type shellInvocation struct {
	CallID       string `json:"call_id,omitempty"`
	JournalToken string `json:"journal_token,omitempty"`
}

type shellInvocationContextKey struct{}

func (invocation shellInvocation) source(body string) string {
	if invocation == (shellInvocation{}) && !strings.HasPrefix(body, shellInvocationPrefix) &&
		!strings.Contains(body, "\n"+shellInvocationPrefix) {
		return body
	}
	if strings.HasPrefix(body, shellInvocationPrefix) {
		// Escape authored leading markers with the legacy envelope so retained
		// leading frames can always take precedence over their arbitrary body.
		return shellInvocationPrefix + string(mustMarshalJSON(invocation)) + "\n" + body
	}
	return body + "\n" + shellInvocationPrefix + string(mustMarshalJSON(invocation))
}

func parseShellInvocation(source string) (shellInvocation, string, error) {
	var invocation shellInvocation
	var header, body string
	newline := true
	if metadata, marked := strings.CutPrefix(source, shellInvocationPrefix); marked {
		// Retained host carriers may still contain the historical leading frame.
		header, body, newline = strings.Cut(metadata, "\n")
	} else if before, after, found := strings.CutLast(source, "\n"+shellInvocationPrefix); found &&
		!strings.Contains(after, "\n") {
		body = before
		header = after
	} else {
		return invocation, source, nil
	}
	if !newline || json.Unmarshal([]byte(header), &invocation) != nil ||
		(invocation.CallID != "" && !capturer.ValidAXIdentity(invocation.CallID)) {
		return shellInvocation{}, "", errors.New("shell: invalid invocation metadata")
	}
	return invocation, body, nil
}

func (invocation shellInvocation) commentary(sink shellCommentarySink) shellCommentarySink {
	if invocation.JournalToken == "" {
		return sink
	}
	if thread, ok := sink.(*threadShellCommentarySink); ok {
		// Do not mutate a sink shared with another invocation.
		bound := *thread
		bound.token = invocation.JournalToken
		return &bound
	}
	return sink
}

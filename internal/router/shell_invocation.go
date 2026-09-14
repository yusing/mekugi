package router

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/yusing/mekugi/capturer"
)

// Invocation metadata is private framing inside the existing source argument.
// It is removed before header parsing and never enters the program environment.
const shellInvocationPrefix = "# mekugi:invocation="

type shellInvocation struct {
	CallID       string `json:"call_id,omitempty"`
	JournalToken string `json:"journal_token,omitempty"`
}

type shellInvocationContextKey struct{}

func (invocation shellInvocation) source(body string) string {
	if invocation == (shellInvocation{}) && !strings.HasPrefix(body, shellInvocationPrefix) {
		return body
	}
	return shellInvocationPrefix + string(mustMarshalJSON(invocation)) + "\n" + body
}

func parseShellInvocation(source string) (shellInvocation, string, error) {
	var invocation shellInvocation
	metadata, marked := strings.CutPrefix(source, shellInvocationPrefix)
	if !marked {
		return invocation, source, nil
	}
	header, body, newline := strings.Cut(metadata, "\n")
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

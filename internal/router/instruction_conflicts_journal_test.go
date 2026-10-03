package router

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestConflictRewriteTeachesTreeJournal(t *testing.T) {
	stock := strings.Join([]string{
		"Put this explanation in a short, separate paragraph at the end of both commentary and final, after any permission question.",
		"Do NOT put a final response (e.g. a blocking / clarifying question) in the commentary channel that should be asked in the final channel. Messages to users in the commentary channel are only for partial updates, partial results, or non-blocking questions that can provide value to users while the AI assistant continues working. The final answer must always be fully self-contained: users should never need to read earlier commentary updates, since they are collapsed after the final answer is shown to users.",
		"- You share updates in the `commentary` channel.",
		"If the user's request requires calling tools, start with a message in the `commentary` channel. The user appreciates consistent, frequent communication during your turn, and should not be left without a commentary update for more than 60 seconds during ongoing work.",
		"Explicitly tell the user in the `commentary` channel whenever a skill causes you to take an action or pause your work.",
		"If asked mid-task, answer briefly in commentary, then continue.",
	}, "\n")
	request := parsedResponsesRequest{fields: map[string]json.RawMessage{"instructions": mustMarshalJSON(stock)}}
	if err := rewriteRequestInstructionConflicts(&request); err != nil {
		t.Fatal(err)
	}
	got := jsonString(request.fields, "instructions")
	if strings.Contains(got, "commentary") {
		t.Fatalf("stock commentary guidance survived: %q", got)
	}
}

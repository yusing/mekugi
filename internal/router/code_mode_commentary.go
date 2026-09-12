package router

import (
	"cmp"
	"errors"
	"slices"
	"strconv"
)

const codeModeCommentaryHistoryTool = "__mekugi_code_mode_commentary"

type codeModeCommentaryCall struct {
	start         int
	end           int
	argumentStart int
	argumentEnd   int
}

func (t *mekugiResponseTransform) lowerCodeModeCommentary(callID, input string) (string, bool, error) {
	if t.proxy.commentaryEndpoint == "" {
		return input, false, nil
	}
	calls, err := findCodeModeCommentaryCalls(input)
	if err != nil || len(calls) == 0 {
		return input, false, err
	}
	if callID == "" {
		return "", false, errors.New("Code Mode commentary call has no call ID")
	}
	slices.SortFunc(calls, func(first, second codeModeCommentaryCall) int {
		if order := cmp.Compare(first.start, second.start); order != 0 {
			return order
		}
		return cmp.Compare(second.end, first.end)
	})
	parents := make([]int, len(calls))
	for index := range parents {
		parents[index] = -1
	}
	stack := make([]int, 0, len(calls))
	for index, call := range calls {
		for len(stack) != 0 && calls[stack[len(stack)-1]].end <= call.start {
			stack = stack[:len(stack)-1]
		}
		if len(stack) != 0 {
			parent := stack[len(stack)-1]
			if call.start < calls[parent].argumentStart || call.end > calls[parent].argumentEnd {
				return "", false, errors.New("Code Mode commentary calls overlap without nesting")
			}
			parents[index] = parent
		}
		stack = append(stack, index)
	}

	token := t.proxy.commentary.subscribe(t.historySessionID, callID, t.commentaryAuthor)
	if token != "" {
		t.proxy.commentary.bindJournalQuestion(token, t.journalQuestion)
		t.proxy.commentary.bindActivity(token, t.shellThreadID)
		t.commentarySubscriptions = append(t.commentarySubscriptions, commentarySubscription{token: token, callID: callID})
	}
	outcome := "prepared"
	if token == "" {
		t.featureTrace.record("journal", "code_mode", "lowering", "unavailable", callID, "")
		return "", false, errors.New("journal publisher unavailable")
	}
	t.featureTrace.record("journal", "code_mode", "lowering", outcome, callID, "")
	replacements := make([]string, len(calls))
	for index := len(calls) - 1; index >= 0; index-- {
		call := calls[index]
		argument := input[call.argumentStart:call.argumentEnd]
		for child := len(calls) - 1; child > index; child-- {
			if parents[child] != index {
				continue
			}
			childCall := calls[child]
			start := childCall.start - call.argumentStart
			end := childCall.end - call.argumentStart
			argument = argument[:start] + replacements[child] + argument[end:]
		}
		command := workerCommand("shell", []string{
			commentaryOnceArgument,
			t.proxy.commentaryEndpoint,
			token,
		})
		commandExpression := strconv.Quote(command+" '") +
			" + encodeURIComponent(JSON.stringify(mutation)).replaceAll(\"'\", \"%27\") + \"'\""
		replacements[index] = `(await (async mutation => {
let execution = await tools.exec_command({cmd: ` + commandExpression + `, login: false});
let output = execution.output || "";
while (execution.session_id != null) {
  execution = await tools.write_stdin({session_id: execution.session_id, chars: "", yield_time_ms: 10000});
  output += execution.output || "";
}
if (execution.exit_code !== 0) throw new Error("journal publication failed");
const publication = JSON.parse(output);
if (publication.ok !== true || !Array.isArray(publication.items) || publication.items.length !== (Array.isArray(mutation) ? mutation.length : 1) || publication.items.some(id => typeof id !== "string")) {
  throw new Error("invalid journal publication result");
}
return Array.isArray(mutation) ? publication.items : publication.items[0];
})(` + argument + `))`
	}

	result := input
	for index, call := range slices.Backward(calls) {
		if parents[index] != -1 {
			continue
		}

		result = result[:call.start] + replacements[index] + result[call.end:]
	}
	return result, true, nil
}

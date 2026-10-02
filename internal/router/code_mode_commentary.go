package router

import (
	"cmp"
	"errors"
	"slices"
	"strconv"
	"strings"
)

type codeModeCommentaryCall struct {
	start         int
	end           int
	argumentStart int
	argumentEnd   int
}

const journalPublisherUnavailable = "journal publisher unavailable; finish outstanding calls, then retry await journal(...)"

func (t *mekugiResponseTransform) lowerCodeModeCommentary(callID, input string) (string, bool, error) {
	if t.proxy.commentaryEndpoint == "" {
		return input, false, nil
	}
	calls, err := findCodeModeCommentaryCalls(input)
	if err != nil || len(calls) == 0 {
		return input, false, err
	}
	if callID == "" {
		return "", false, errors.New("code Mode commentary call has no call ID")
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
				return "", false, errors.New("code Mode commentary calls overlap without nesting")
			}
			parents[index] = parent
		}
		stack = append(stack, index)
	}

	token := t.proxy.commentary.subscribe(t.historySessionID, callID)
	if token != "" {
		t.proxy.commentary.bindJournalQuestion(token, t.journalQuestion)
		t.proxy.commentary.bindActivity(token, t.shellThreadID)
		t.proxy.commentary.bindJournalFinish(token, t.shellTurnID)
		t.commentarySubscriptions = append(t.commentarySubscriptions, commentarySubscription{token: token, callID: callID})
	}
	outcome := "prepared"
	if token == "" {
		t.featureTrace.record("journal", "code_mode", "lowering", "unavailable", callID, "")
	} else {
		t.featureTrace.record("journal", "code_mode", "lowering", outcome, callID, "")
	}
	_, suffix, _ := strings.Cut(token, ".")
	helperName := "__mekugiJournal_" + strings.ReplaceAll(suffix, "-", "_")
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
		if token == "" {
			// Let the host execute the normal carrier. The failed journal helper
			// becomes a model-visible tool error, not a router translation fault.
			replacements[index] = `((async mutation => { throw new Error(` + strconv.Quote(journalPublisherUnavailable) + `); })(` + argument + `))`
			continue
		}
		replacements[index] = helperName + `(` + argument + `)`
	}

	result := input
	for index, call := range slices.Backward(calls) {
		if parents[index] != -1 {
			continue
		}

		result = result[:call.start] + replacements[index] + result[call.end:]
	}
	if token == "" {
		return result, true, nil
	}
	// A rejected mutation applied nothing. The helper reports it through
	// text() and returns no paths, so the program's remaining work still runs.
	command := workerCommand("mjournal", []string{
		commentaryOnceArgument,
		t.proxy.commentaryEndpoint,
		token,
	})
	commandExpression := strconv.Quote(command+" '") +
		" + encodeURIComponent(JSON.stringify(mutation)).replaceAll(\"'\", \"%27\") + \"'\""
	helper := "\nasync function " + helperName + `(mutation) {
const operation = !Array.isArray(mutation) && (mutation.op === "read" || mutation.op === "list") ? mutation.op : "mutation";
try {
const command = ` + commandExpression + `;
const items = [];
let continuation = "";
while (true) {
let execution = await tools.exec_command({cmd: command + continuation, login: false});
let output = execution.output || "";
while (execution.session_id != null) {
  execution = await tools.write_stdin({session_id: execution.session_id, chars: "", yield_time_ms: 10000});
  output += execution.output || "";
}
if (execution.exit_code !== 0) throw new Error("transport exited " + execution.exit_code + (output.trim() ? ": " + output.trim().slice(0, 16384) : ""));
const publication = JSON.parse(output);
if (publication.ok === false && typeof publication.error === "string") {
  if (typeof globalThis.text !== "function") throw new Error(publication.error);
  globalThis.text(publication.error);
  return Array.isArray(mutation) || mutation.op === "plan" ? [] : null;
}
if (publication.ok !== true || !Array.isArray(publication.items)) throw new Error("invalid journal result");
if (!Array.isArray(mutation) && (mutation.op === "list" || mutation.op === "read")) {
  items.push(...publication.items);
  if (publication.next == null) {
    if (mutation.op === "list") return items;
    const nodes = new Map(items.map(node => [node.path, node]));
    const roots = [];
    for (const node of items) {
      const parent = nodes.get(node.path.slice(0, node.path.lastIndexOf("/")));
      if (parent) parent.children.push(node); else roots.push(node);
    }
    return roots;
  }
  if (!Number.isInteger(publication.next) || publication.next !== items.length || !/^[a-f0-9]{64}$/.test(publication.revision)) throw new Error("invalid journal read continuation");
  continuation = " " + publication.next + " " + publication.revision;
  continue;
}
if (publication.items.some(id => typeof id !== "string")) {
  throw new Error("invalid journal result");
}
if ((Array.isArray(mutation) ? mutation.some(op => op.op === "plan") : mutation.op === "plan") && typeof globalThis.text === "function") {
  globalThis.text("journal paths: " + JSON.stringify(publication.items));
}
return Array.isArray(mutation) || mutation.op === "plan" ? publication.items : publication.items[0] ?? null;
}
} catch (error) {
  throw new Error("journal " + operation + " failed: " + (error instanceof Error ? error.message : String(error)));
}
}
`
	return result + helper, true, nil
}

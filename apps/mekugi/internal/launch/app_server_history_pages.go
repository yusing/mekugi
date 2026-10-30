package launch

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/backends/codex/binding"
)

// Paginated resume/revert replies carry metadata, not the retained transcript.
// Keep the original response correlated until every history page is admitted.
type appServerHistoryRead struct {
	message  appserver.Message
	thread   binding.ThreadInfo
	cursors  map[string]bool
	complete bool
	failure  string
}

func hydrateHistory(control *sessioncontrol.Client[appServerRequest], message appserver.Message) (bool, *appserver.Message, error) {
	request, ok := control.Requests.Lookup(string(message.ID))
	if !ok || message.Method != "" {
		return false, nil, nil
	}
	if request.Owner.label == "history/page" {
		control.Requests.Take(string(message.ID))
		read := request.Owner.history
		if message.Error != nil {
			read.failure = message.Error.Message
			return finishHistoryRead(read)
		}
		var page appServerHistoryPage
		if err := json.Unmarshal(message.Result, &page); err != nil {
			read.failure = err.Error()
			return finishHistoryRead(read)
		}
		read.thread.Turns = append(read.thread.Turns, page.Data...)
		if page.NextCursor != "" {
			if read.cursors[page.NextCursor] {
				read.failure = "retained conversation cursor did not advance"
				return finishHistoryRead(read)
			}
			read.cursors[page.NextCursor] = true
			_, err := control.Requests.Send("thread/turns/list", historyPageParams(read.thread.ID, page.NextCursor), "main", request.Owner)
			if err != nil {
				read.failure = err.Error()
				return finishHistoryRead(read)
			}
			return true, nil, nil
		}
		return finishHistoryRead(read)
	}
	if request.Owner.history != nil {
		return !request.Owner.history.complete, nil, nil
	}
	if message.Error != nil || (request.Method != "thread/resume" && request.Method != "thread/fork" && request.Method != "thread/revert") || request.Side != nil || request.Prediction != nil {
		return false, nil, nil
	}
	result, err := control.Startup.Accept(&control.Live, request.Method, message.Result)
	if err != nil || result.Thread.HistoryMode != "paginated" {
		return false, nil, nil
	}
	read := &appServerHistoryRead{message: message, thread: result.Thread, cursors: make(map[string]bool)}
	read.thread.Turns = nil
	request.Owner.history = read
	control.Requests.Attribute(string(message.ID), request.Scope, request.Owner)
	_, err = control.Requests.Send("thread/turns/list", historyPageParams(read.thread.ID, ""), "main", appServerRequest{label: "history/page", history: read})
	if err != nil {
		read.failure = err.Error()
		return finishHistoryRead(read)
	}
	return true, nil, nil
}

// Failure to display retained history must not discard an accepted native
// operation or retry it. Publish the known prefix with an explicit gap instead.
func finishHistoryRead(read *appServerHistoryRead) (bool, *appserver.Message, error) {
	var result map[string]jsontext.Value
	if err := json.Unmarshal(read.message.Result, &result); err != nil {
		return true, nil, err
	}
	var err error
	result["thread"], err = json.Marshal(read.thread)
	if err == nil {
		read.message.Result, err = json.Marshal(result)
	}
	read.complete = true
	return true, &read.message, err
}

func historyPageParams(thread, cursor string) map[string]any {
	params := map[string]any{"threadId": thread, "sortDirection": "asc", "itemsView": "full", "limit": 100}
	if cursor != "" {
		params["cursor"] = cursor
	}
	return params
}

type appServerHistoryPage struct {
	Data       []binding.HistoryTurn `json:"data"`
	NextCursor string                `json:"nextCursor"`
}

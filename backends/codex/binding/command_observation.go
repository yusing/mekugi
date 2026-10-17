package binding

// Source: apps/mekugi/internal/router/exec_track.go:143:157 and
// apps/mekugi/internal/router/app_server_events.go:755:791@b27b4c66.

import (
	"cmp"
	"encoding/json/jsontext"
	json "encoding/json/v2"
)

// CommandObservation preserves command identity and supplied output/outcome.
// Decoding confers neither execution nor report-correlation authority.
type CommandObservation struct {
	Key            [3]string // Thread, turn, item.
	Command, Delta string
	Output         *string
	Exit           *int
}

func DecodeCommandObservation(method string, raw jsontext.Value) (CommandObservation, bool) {
	if method != "item/started" && method != "item/completed" && method != "item/commandExecution/outputDelta" {
		return CommandObservation{}, false
	}
	var event struct {
		Thread string `json:"threadId"`
		Turn   string `json:"turnId"`
		ItemID string `json:"itemId"`
		Delta  string `json:"delta"`
		Item   struct {
			ID      string  `json:"id"`
			Type    string  `json:"type"`
			Command string  `json:"command"`
			Output  *string `json:"aggregatedOutput"`
			Exit    *int    `json:"exitCode"`
		} `json:"item"`
	}
	if json.Unmarshal(raw, &event) != nil || method != "item/commandExecution/outputDelta" && event.Item.Type != "commandExecution" {
		return CommandObservation{}, false
	}
	return CommandObservation{Key: [3]string{event.Thread, event.Turn, cmp.Or(event.ItemID, event.Item.ID)}, Command: event.Item.Command, Delta: event.Delta, Output: event.Item.Output, Exit: event.Item.Exit}, true
}

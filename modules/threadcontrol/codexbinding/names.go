package codexbinding

import (
	"bytes"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"strings"

	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

// Only stock identity sentences are adapted; custom hints bypass this projection.
func visibleRoleNames(text string) string {
	rows := strings.Split(text, "\n")
	for i, row := range rows {
		switch row {
		case "You are `/root`, the primary agent in a team of agents collaborating to fulfill the user's goals.",
			"They may be addressed as to=/root",
			"You may also see them addressed as to=/root/..., which indicates your identity is /root/...":
			rows[i] = strings.ReplaceAll(row, "/root", "agent:main")
		}
	}
	return strings.Join(rows, "\n")
}

// visibleResultNames projects only owned coordination-result fields, never message
// payloads or arbitrary strings. Canonical values remain in retained owner state.
func visibleResultNames(raw []byte) ([]byte, error) {
	if !bytes.Contains(raw, []byte("/root")) {
		return raw, nil
	}
	var fields map[string]jsontext.Value
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	changed, err := projectResultFields(fields)
	if err != nil || !changed {
		return raw, err
	}
	return responses.MustMarshalJSON(fields), nil
}

func projectResultFields(fields map[string]jsontext.Value) (bool, error) {
	changed := false
	for key, value := range fields {
		if !bytes.Contains(value, []byte("/root")) {
			continue
		}
		var projected []byte
		var err error
		switch key {
		case "task", "task_name", "agent_name":
			name := responses.StringField(fields, key)
			if visible := control.AgentName(name); visible != name {
				projected = responses.MustMarshalJSON(visible)
			}
		case "targets":
			var names []string
			if err = json.Unmarshal(value, &names); err == nil {
				for i, name := range names {
					names[i] = control.AgentName(name)
				}
				projected = responses.MustMarshalJSON(names)
			}
		case "metrics":
			var metrics map[string]jsontext.Value
			if err = json.Unmarshal(value, &metrics); err == nil {
				for name, metric := range metrics {
					if visible := control.AgentName(name); visible != name {
						delete(metrics, name)
						metrics[visible] = metric
					}
				}
				projected = responses.MustMarshalJSON(metrics)
			}
		case "agents", "effects", "events":
			var rows []jsontext.Value
			if err = json.Unmarshal(value, &rows); err == nil {
				for i := range rows {
					rows[i], err = visibleResultNames(rows[i])
					if err != nil {
						return false, err
					}
				}
				projected = responses.MustMarshalJSON(rows)
			}
		case "event":
			projected, err = visibleResultNames(value)
		}
		if err != nil {
			return false, err
		}
		if projected != nil && !responses.SameJSONValue(projected, value) {
			fields[key], changed = projected, true
		}
	}
	return changed, nil
}

// Authenticated message envelopes have two address rows before the payload.
func visibleMessageNames(text string) string {
	header, payload, ok := strings.Cut(text, "\nPayload:\n")
	if !ok {
		return text
	}
	rows := strings.Split(header, "\n")
	start := 0
	if strings.HasPrefix(rows[0], "Operation ID: ") {
		start = 1
	}
	if len(rows) < start+3 || (rows[start] != "Message Type: NEW_TASK" && rows[start] != "Message Type: MESSAGE" && rows[start] != "Message Type: FINAL_ANSWER") {
		return text
	}
	for i, prefix := range []string{"Task name: ", "Sender: "} {
		name, ok := strings.CutPrefix(rows[start+i+1], prefix)
		if !ok {
			return text
		}
		rows[start+i+1] = prefix + control.AgentName(name)
	}
	return strings.Join(rows, "\n") + "\nPayload:\n" + payload
}

func visibleCompletionNames(text string) string {
	operation, message, ok := strings.Cut(text, "\nActive descendants: ")
	if !ok || !strings.HasPrefix(operation, "Operation ID: ") {
		return text
	}
	names, ok := strings.CutSuffix(message, ". Call wait_agent before completing.")
	if !ok {
		return text
	}
	parts := strings.Split(names, ", ")
	for i, name := range parts {
		parts[i] = control.AgentName(name)
	}
	return operation + "\nActive descendants: " + strings.Join(parts, ", ") + ". Call wait_agent before completing."
}

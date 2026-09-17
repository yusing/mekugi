package router

import (
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"strconv"
	"strings"
)

const minimumStatusWaitMS = 300000

func statusWaitFloor(kind string, maximumJSON []byte) int {
	if kind != "integer" && kind != "number" {
		return 0
	}
	floor := minimumStatusWaitMS
	var maximum float64
	if json.Unmarshal(maximumJSON, &maximum) == nil && maximum > 0 && maximum < float64(floor) {
		floor = int(maximum)
	}
	return floor
}

// waitPolicy changes only the requested blocking timeout. Codex still owns
// execution, early completion, cancellation, and its runtime timeout cap.
type waitPolicy struct {
	field string
	floor int
}

type waitPolicies struct {
	direct map[string]waitPolicy
	nested map[string]waitPolicy
}

func collectWaitPolicies(catalog *responsesToolCatalog, execName string) waitPolicies {
	result := waitPolicies{direct: make(map[string]waitPolicy), nested: make(map[string]waitPolicy)}
	var visit func(*responsesToolSection, string)
	visit = func(section *responsesToolSection, namespace string) {
		if section == nil || section.err != nil {
			return
		}
		for _, tool := range section.tools {
			if tool == nil {
				continue
			}
			if tool.Type == "namespace" {
				visit(tool.nested, qualifiedToolName(namespace, tool.Name))
				continue
			}
			if tool.Type == "custom" && tool.Name == execName {
				if section := codeModeSessionWaitSection(tool.Description); hasCodeModeSessionWait(tool.Description) && strings.Contains(section, "yield_time_ms") {
					result.nested["write_stdin"] = waitPolicy{"yield_time_ms", minimumStatusWaitMS}
				}
			}
			if tool.Type != "function" {
				continue
			}
			base := strings.TrimPrefix(tool.Name, "functions.")
			field := ""
			switch {
			case (namespace == "" || namespace == "functions") && (base == "wait" || base == "write_stdin"):
				field = "yield_time_ms"
			case (namespace == "" || namespace == "functions" || namespace == "collaboration" || namespace == "mekugi_collaboration" || namespace == "native_agents") && base == "wait_agent":
				field = "timeout_ms"
			}
			if field == "" {
				continue
			}
			var schema struct {
				Properties map[string]struct {
					Type    string            `json:"type"`
					Maximum jsonv1.RawMessage `json:"maximum"`
				} `json:"properties"`
			}
			if json.Unmarshal(tool.rawField("parameters"), &schema) != nil {
				continue
			}
			if base == "wait" && schema.Properties["cell_id"].Type != "string" {
				continue
			}
			if base == "write_stdin" {
				idType := schema.Properties["session_id"].Type
				if (idType != "integer" && idType != "number") || schema.Properties["chars"].Type != "string" {
					continue
				}
			}
			timing := schema.Properties[field]
			floor := statusWaitFloor(timing.Type, timing.Maximum)
			if floor > 0 {
				result.direct[functionToolKey(namespace, tool.Name)] = waitPolicy{field, floor}
			}
		}
	}
	visit(catalog.top, "")
	for _, group := range catalog.additional {
		visit(group.tools, "")
	}
	return result
}

func (p waitPolicy) rewrite(name, input string) string {
	var args map[string]jsonv1.RawMessage
	if json.Unmarshal([]byte(input), &args) != nil || args == nil {
		return input // Keep native validation authoritative.
	}
	if strings.TrimPrefix(name, "functions.") == "write_stdin" {
		if raw, present := args["chars"]; present && string(raw) != `""` {
			return input
		}
	}
	if raw, present := args["terminate"]; present && string(raw) != "false" {
		return input
	}
	if raw, present := args[p.field]; present {
		var value float64
		if string(raw) == "null" || json.Unmarshal(raw, &value) != nil || value < 0 || value >= float64(p.floor) {
			return input
		}
	}
	args[p.field] = jsonv1.RawMessage(strconv.Itoa(p.floor))
	encoded, err := json.Marshal(args, json.Deterministic(true))
	if err != nil {
		return input
	}
	return string(encoded)
}

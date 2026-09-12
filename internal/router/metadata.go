package router

// Source: routing_context.go:18:219 Codex metadata and base-directory handling.

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const codexTurnMetadataHeader = "x-codex-turn-metadata"

type codexTurnMetadata struct {
	activityIdentityInvalid bool
	ThreadID                string                     `json:"thread_id"`
	ForkedFromThreadID      string                     `json:"forked_from_thread_id"`
	ParentThreadID          string                     `json:"parent_thread_id"`
	AgentName               string                     `json:"agent_name"`
	RequestKind             string                     `json:"request_kind"`
	TurnID                  string                     `json:"turn_id"`
	SubagentKind            string                     `json:"subagent_kind"`
	Directories             map[string]json.RawMessage `json:"workspaces"`
	Compaction              json.RawMessage            `json:"compaction"`
}

func decodeCodexTurnMetadata(headers http.Header) (codexTurnMetadata, bool) {
	values := []string{}
	for name, headerValues := range headers {
		if !strings.EqualFold(name, codexTurnMetadataHeader) {
			continue
		}
		for _, value := range headerValues {
			if strings.TrimSpace(value) == "" {
				continue
			}
			if !slices.Contains(values, value) {
				values = append(values, value)
			}
		}
	}
	if len(values) != 1 || !isASCII(values[0]) {
		return codexTurnMetadata{}, false
	}

	trimmed := strings.TrimLeft(values[0], " \t\r\n")
	if !strings.HasPrefix(trimmed, "{") {
		return codexTurnMetadata{}, false
	}
	decoder := json.NewDecoder(strings.NewReader(values[0]))
	var metadata codexTurnMetadata
	// Auxiliary ancestry fields must not turn an otherwise valid request into a
	// transport failure. Malformed identities disable projection only.
	wire := struct {
		*codexTurnMetadata
		ThreadID       json.RawMessage `json:"thread_id"`
		AgentName      json.RawMessage `json:"agent_name"`
		ParentThreadID json.RawMessage `json:"parent_thread_id"`
	}{codexTurnMetadata: &metadata}
	if err := decoder.Decode(&wire); err != nil {
		return codexTurnMetadata{}, false
	}
	for _, field := range []struct {
		raw   json.RawMessage
		value *string
	}{{wire.ThreadID, &metadata.ThreadID}, {wire.ParentThreadID, &metadata.ParentThreadID}, {wire.AgentName, &metadata.AgentName}} {
		if len(field.raw) != 0 && (string(field.raw) == "null" || json.Unmarshal(field.raw, field.value) != nil) {
			metadata.activityIdentityInvalid = true
		}
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return codexTurnMetadata{}, false
	}
	return metadata, true
}

func isASCII(value string) bool {
	for index := range len(value) {
		if value[index] > 0x7f {
			return false
		}
	}
	return true
}

func usableRoutingDirectory(declared map[string]json.RawMessage) (string, bool) {
	for path := range declared {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			continue
		}
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			continue
		}
		canonical, err := filepath.EvalSymlinks(path)
		if err != nil || !filepath.IsAbs(canonical) {
			continue
		}
		return canonical, true
	}
	return "", false
}

// Only child requests carry an operation author. Older clients may omit the name.
func (m codexTurnMetadata) commentaryAuthor() string {
	if m.SubagentKind == "" || !strings.HasPrefix(m.AgentName, "/root/") {
		return ""
	}
	return m.AgentName
}

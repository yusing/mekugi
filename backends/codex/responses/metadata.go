package responses

import (
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"net/http"
	"slices"
	"strings"

	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

// Source: apps/mekugi/internal/router/metadata.go:21:118@b9679c93.
// Host metadata retains its permissive v1 object-decoding contract.
type TurnMetadata struct {
	ActivityIdentityInvalid bool                         `json:"-"`
	ThreadID                string                       `json:"thread_id"`
	ForkedFromThreadID      string                       `json:"forked_from_thread_id"`
	ParentThreadID          string                       `json:"parent_thread_id"`
	AgentName               string                       `json:"agent_name"`
	RequestKind             responses.RequestKind        `json:"request_kind"`
	TurnID                  string                       `json:"turn_id"`
	SubagentKind            string                       `json:"subagent_kind"`
	Directories             map[string]jsonv1.RawMessage `json:"workspaces"`
	Compaction              jsonv1.RawMessage            `json:"compaction"`
}

// ApprovalReview identifies Codex's internal reviewer, not a managed agent.
// This selects request projection only; Codex retains approval authority.
func ApprovalReview(headers http.Header, metadata TurnMetadata) bool {
	return headers.Get(GuardianHeader) == "reviewer" ||
		headers.Get(SubagentHeader) == "guardian" || metadata.SubagentKind == "guardian"
}

func DecodeTurnMetadata(headers http.Header) (TurnMetadata, bool) {
	values := []string{}
	for name, headerValues := range headers {
		if !strings.EqualFold(name, TurnMetadataHeader) {
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
		return TurnMetadata{}, false
	}

	trimmed := strings.TrimLeft(values[0], " \t\r\n")
	if !strings.HasPrefix(trimmed, "{") {
		return TurnMetadata{}, false
	}
	var metadata TurnMetadata
	// Auxiliary ancestry fields must not turn an otherwise valid request into a
	// transport failure. Malformed identities disable projection only.
	wire := struct {
		*TurnMetadata
		ThreadID       jsonv1.RawMessage `json:"thread_id"`
		AgentName      jsonv1.RawMessage `json:"agent_name"`
		ParentThreadID jsonv1.RawMessage `json:"parent_thread_id"`
	}{TurnMetadata: &metadata}
	if err := json.Unmarshal([]byte(values[0]), &wire, jsonv1.DefaultOptionsV1()); err != nil {
		return TurnMetadata{}, false
	}
	for _, field := range []struct {
		raw   jsonv1.RawMessage
		value *string
	}{{wire.ThreadID, &metadata.ThreadID}, {wire.ParentThreadID, &metadata.ParentThreadID}, {wire.AgentName, &metadata.AgentName}} {
		if len(field.raw) != 0 && (string(field.raw) == "null" || json.Unmarshal(field.raw, field.value, jsonv1.DefaultOptionsV1()) != nil) {
			metadata.ActivityIdentityInvalid = true
		}
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

// Only child requests carry an operation author. Older clients may omit the name.
func (m TurnMetadata) CommentaryAuthor() string {
	if m.SubagentKind == "" || !strings.HasPrefix(m.AgentName, "/root/") {
		return ""
	}
	return m.AgentName
}

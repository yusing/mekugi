// Package commentaryid owns the reserved IDs used for router-generated messages.
package commentaryid

import "strings"

const (
	OperationPrefix  = "msg_mekugi_commentary_"
	SubagentPrefix   = "msg_mekugi_subagent_commentary_"
	CompactionPrefix = "msg_mekugi_compaction_"
)

// Generated reports membership in a router-owned message ID namespace. Message
// text and phase are not provenance: a model may emit identical commentary.
func Generated(id string) bool {
	return strings.HasPrefix(id, OperationPrefix) || strings.HasPrefix(id, SubagentPrefix) || strings.HasPrefix(id, CompactionPrefix)
}

package router

import (
	"crypto/sha256"
	"fmt"
	"strings"
)

func qualifiedToolName(namespace, name string) string {
	if namespace == "" {
		return name
	}
	return namespace + "." + name
}

func commentaryMessageID(seed string) string {
	digest := sha256.Sum256([]byte(seed))
	return fmt.Sprintf("msg_mekugi_commentary_%x", digest[:12])
}

func (p *mekugiProxy) commentaryMessageIDs(sessionID string) map[string]struct{} {
	p.mu.RLock()
	defer p.mu.RUnlock()
	result := make(map[string]struct{})
	if session := p.sessions[sessionID]; session != nil {
		for _, history := range session.calls {
			for _, messageID := range history.CommentaryMessageIDs {
				result[messageID] = struct{}{}
			}
		}
	}
	return result
}

// commentaryCode keeps backticks in names or previews from ending the code span.
func commentaryCode(value string) string {
	longest, run := 0, 0
	for _, r := range value {
		if r == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	fence := strings.Repeat("`", longest+1)
	if longest > 0 || strings.HasPrefix(value, " ") || strings.HasSuffix(value, " ") {
		return fence + " " + value + " " + fence
	}
	return fence + value + fence
}

package agentguidance

import "strings"

// CompactInstructions supplements the caller's handover only during plaintext compaction.
const CompactInstructions = `
At the end of your handover emit exactly one ` + CompactMetadataStart + `JSON` + CompactMetadataEnd + ` block on a new line. Select reload references, schema: {"sources":[{"path":"src/example.go","ranges":[[10,40]]},{"skill":"active-skill","path":"/absolute/SKILL.md"},{"skill":"managed-skill"},{"changes":["amber1..amber3","birch2"]}]}.
Select the context needed for the next unfinished operation: applicable task guides and their procedure references, active skills and their required references, and the implementation, callers, tests or fixtures already inspected that the next step depends on. Include known supporting files, not just entry points or owner maps, so continuation need not rediscover them. Use focused ranges for large files. Keep applicable guidance selected when complete or waiting for input; emit {"sources":[]} only when no guidance or unfinished-work context is needed. Exclude AGENTS.md at every directory level; Codex supplies it.
Skill entries contain only the skill name and, for native skills, the SKILL.md path; managed skills omit path. Required skill references use file entries. File paths resolve from the current workspace; ranges are inclusive and may be omitted for a whole file. Captured-change entries contain only changes, an array of explicit mchanges IDs or same-agent inclusive ranges; each supplies their complete composed diff. Select known receipts needed for unfinished work. Do not reproduce source bodies in the handover or metadata. Mekugi separates this metadata from the handover, performs authorized reads, and injects reusable read context before continuation. Do not write a selection file or update selections during ordinary turns.
`

const CompactMetadataStart = "<mekugi-reload-v1>"
const CompactMetadataEnd = "</mekugi-reload-v1>"

// SplitCompactReload separates the terminal, line-start reload block from its
// handover without depending on the host's summary introduction.
func SplitCompactReload(text string) (handoff, metadata string, ok bool) {
	handoff, rest, found := strings.CutLast(text, "\n"+CompactMetadataStart)
	if !found {
		return text, "", false
	}
	metadata, ended := strings.CutSuffix(strings.TrimSpace(rest), CompactMetadataEnd)
	if !ended {
		return text, "", false
	}
	return handoff + "\n", metadata, true
}

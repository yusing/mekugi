package agentguidance

import "strings"

// CompactInstructions supplements the caller's handover only during plaintext compaction.
const CompactInstructions = `
At the end of your handover emit exactly one ` + CompactMetadataStart + `JSON` + CompactMetadataEnd + ` block on a new line. Select reload references, schema: {"sources":[{"path":"src/example.go","ranges":[[10,40]]},{"skill":"active-skill","path":"/absolute/SKILL.md"},{"skill":"managed-skill"}]}. Select applicable instruction-file references, active skill references, and focused task-file references needed after compaction. Skill entries contain only the skill name and, for native skills, the SKILL.md path; managed skills omit path. Required skill references use file entries. File paths resolve from the current workspace; ranges are inclusive and may be omitted for a whole file. Do not reproduce file contents or skill instructions in the handover or metadata. Mekugi separates this metadata from the handover, performs authorized fresh reads, and injects reusable read context before continuation. Emit {"sources":[]} when no reload references are needed. Do not write a selection file or update selections during ordinary turns.
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

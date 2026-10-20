package inferencegateway

import _ "embed"

//go:embed instructions/opencode_spawn.md
var opencodeSpawnInstruction string

//go:embed instructions/grok_model.md
var grokModelInstruction string

//go:embed instructions/grok_fork_turns.md
var grokForkTurnsInstruction string

//go:embed instructions/grok_reasoning_effort.md
var grokReasoningEffortInstruction string

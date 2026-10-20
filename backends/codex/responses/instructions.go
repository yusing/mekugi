package responses

import _ "embed"

//go:embed instructions/custom_input.md
var customInputInstruction string

//go:embed instructions/exec.md
var execInstruction string

//go:embed instructions/format_prefix.md
var formatPrefixInstruction string

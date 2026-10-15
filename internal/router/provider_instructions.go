package router

import _ "embed"

//go:embed instructions/grok_input.md
var grokInputInstruction string

//go:embed instructions/grok_exec.md
var grokExecInstruction string

//go:embed instructions/grok_format_prefix.md
var grokFormatPrefixInstruction string

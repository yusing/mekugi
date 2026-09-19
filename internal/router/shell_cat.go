package router

import (
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
	"strings"
)

func shellCatLiteral(word *syntax.Word) (string, bool) {
	// Validate before expansion: process substitutions require an execution
	// callback, and presentation/planning must never invoke one.
	if word == nil || !shellCatLiteralParts(word.Parts, false) {
		return "", false
	}
	value, err := expand.Literal(&expand.Config{}, word)
	return value, err == nil
}

func shellCatLiteralParts(parts []syntax.WordPart, quoted bool) bool {
	for _, part := range parts {
		switch value := part.(type) {
		case *syntax.SglQuoted:
		case *syntax.DblQuoted:
			if !shellCatLiteralParts(value.Parts, true) {
				return false
			}
		case *syntax.Lit:
			if !quoted && strings.ContainsAny(value.Value, "~*?[") {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// Await host-owned sessions before the next statement, without exposing helper
// results or patch success text in the original shell result. An enclosing Code
// Mode cell may yield normally while these awaited calls remain in flight.
const shellCatSequenceRuntime = `let output = '';
let last = {output: '', exit_code: 0};
async function finish(args, forward = false) {
  let result = await tools.exec_command(args);
  if (forward) { last = result; output += result.output || ''; }
  while (result.session_id != null) {
    const continuation = {session_id: result.session_id, chars: '', yield_time_ms: 300000};
    if (args.max_output_tokens != null) continuation.max_output_tokens = args.max_output_tokens;
    result = await tools.write_stdin(continuation);
    if (forward) { last = result; output += result.output || ''; }
  }
  return result;
}
async function run(args) {
  await finish(args, true);
}
`

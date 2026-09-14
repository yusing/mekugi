package router

import (
	"encoding/json"
	"strings"

	"github.com/yusing/mekugi/capturer"
	"mvdan.cc/sh/v3/syntax"
)

const axCarrierCallIDPrefix = "# mekugi:ax:call_id="

// Read explicit router-owned invocation metadata, not arbitrary environment
// assignments. Historical outer markers remain readable for retained rollouts.
// This is local correlation evidence, not authentication of a supplied rollout.
func inspectionAXCallID(encoded json.RawMessage) string {
	var command string
	if json.Unmarshal(encoded, &command) != nil {
		var argv []string
		if json.Unmarshal(encoded, &argv) != nil || len(argv) != 3 ||
			(shellInterpreterName(argv[0]) != "bash" && shellInterpreterName(argv[0]) != "sh") ||
			(argv[1] != "-c" && argv[1] != "-lc") {
			return ""
		}
		command = argv[2]
	}
	metadata, body, newline := strings.Cut(command, "\n")
	identity, marked := strings.CutPrefix(metadata, axCarrierCallIDPrefix)
	if marked {
		if !newline || !capturer.ValidAXIdentity(identity) {
			return ""
		}
		command = body
	}
	program, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil || len(program.Stmts) == 0 {
		return ""
	}
	if marked {
		return identity
	}
	var callID string
	ambiguous := false
	syntax.Walk(program, func(node syntax.Node) bool {
		call, ok := node.(*syntax.CallExpr)
		if !ok {
			return true
		}
		source := inspectionShellWorkerSource(call)
		invocation, _, err := parseShellInvocation(source)
		if err != nil || invocation.CallID == "" {
			return true
		}
		if callID != "" && callID != invocation.CallID {
			ambiguous = true
		}
		callID = invocation.CallID
		return true
	})
	if ambiguous {
		return ""
	}
	return callID
}

// Follow only literal, execution-preserving wrappers. Never expand argv or guess
// what an arbitrary executable does with an argument resembling a worker call.
func inspectionShellWorkerSource(call *syntax.CallExpr) string {
	arguments := make([]string, len(call.Args))
	for index, word := range call.Args {
		value, literal := shellCatLiteral(word)
		if !literal {
			return ""
		}
		arguments[index] = value
	}
	for len(arguments) > 0 {
		switch arguments[0] {
		case "shell":
			if len(arguments) >= 3 {
				return arguments[len(arguments)-1]
			}
			return ""
		case "command":
			arguments = arguments[1:]
			for len(arguments) > 0 && strings.HasPrefix(arguments[0], "-") {
				option := arguments[0]
				arguments = arguments[1:]
				if option == "--" {
					break
				}
				if option != "-p" {
					return ""
				}
			}
		case "env":
			arguments = arguments[1:]
			options := true
		envArguments:
			for len(arguments) > 0 {
				value := arguments[0]
				if options && strings.HasPrefix(value, "-") {
					switch {
					case value == "--":
						options = false
						arguments = arguments[1:]
					case value == "-", value == "-i", value == "--ignore-environment":
						arguments = arguments[1:]
					case value == "-u", value == "--unset", value == "-C", value == "--chdir":
						if len(arguments) < 2 {
							return ""
						}
						arguments = arguments[2:]
					case strings.HasPrefix(value, "--unset="), strings.HasPrefix(value, "--chdir="):
						arguments = arguments[1:]
					default:
						return ""
					}
					continue
				}
				if strings.IndexByte(value, '=') > 0 {
					arguments = arguments[1:]
					options = false
					continue
				}
				break envArguments
			}
		default:
			return ""
		}
	}
	return ""
}

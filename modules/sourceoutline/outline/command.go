package outline

import "github.com/yusing/mekugi/contracts/command"

type Service struct{}

var Command = command.Definition[Service]{Spec: command.Spec{Name: "inspect_file", Description: "Inspect host-readable regular files and return compact structural rows: START-END KIND NAME. Imports collapse to one range; parse_error rows locate syntax errors.\nUsage: inspect_file [--json] [--max-tokens N] PATH [PATH ...]\nMultiple files have path headers and share one budget. Failed paths report errors on stderr (JSON envelopes with --json), preserve successful stdout, and exit nonzero. --json returns metadata and structured outline entries instead. Line ranges are one-based. Example: inspect_file src/main.go, then mcat src/main.go START:END for the relevant entry. Default token budget: 4000."}, ObserveRead: true, Run: Service.Execute}

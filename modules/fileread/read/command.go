package read

import "github.com/yusing/mekugi/contracts/command"

type Service struct{}

var Command = command.Definition[Service]{Spec: command.Spec{Name: "mcat", Description: "Read one or more UTF-8 files or inclusive logical-line ranges as raw rows; --number prefixes source line numbers like nl -ba.\nUsage: mcat [-n N] [--max-tokens N] [--tail] [--number] PATH [START:END ...] [PATH [START:END ...] ...]\n\nSTART:END or START-END is a separate operand after its path, inclusive of both endpoints. Several ranges may follow one path; at most 16 reads are allowed. -n counts rows within a single range and retains the default token ceiling.\nExamples:\n  mcat src/main.go 100:150                # rows 100–150 (51 rows)\n  mcat -n 20 src/main.go                  # first 20 rows\n  mcat --number src/main.go 100:150       # source line prefixes\n  mcat src/main.go 10:40 src/config.go 1:30\n\nLimited output contains complete rows and exits nonzero; retained omissions provide per-file mread recovery."}, ObserveRead: true, Run: Service.Execute}

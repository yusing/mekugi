package binding

// Source: apps/mekugi/internal/router/exec_track.go:143:157 and
// apps/mekugi/internal/router/app_server_events.go:755:791@b27b4c66.

import (
	"cmp"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"path/filepath"
	"strings"

	"github.com/yusing/mekugi/libraries/commandanalysis/shellsyntax"
	"github.com/yusing/mekugi/libraries/shellinstrumentation"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

// CommandObservation preserves command identity and supplied output/outcome.
// Decoding confers neither execution nor report-correlation authority.
type CommandObservation struct {
	Key            [3]string // Thread, turn, item.
	Command, Delta string
	Output         *string
	Exit           *int
}

func DecodeCommandObservation(method string, raw jsontext.Value) (CommandObservation, bool) {
	if method != "item/started" && method != "item/completed" && method != "item/commandExecution/outputDelta" {
		return CommandObservation{}, false
	}
	var event struct {
		Thread string `json:"threadId"`
		Turn   string `json:"turnId"`
		ItemID string `json:"itemId"`
		Delta  string `json:"delta"`
		Item   struct {
			ID      string  `json:"id"`
			Type    string  `json:"type"`
			Command string  `json:"command"`
			Output  *string `json:"aggregatedOutput"`
			Exit    *int    `json:"exitCode"`
		} `json:"item"`
	}
	if json.Unmarshal(raw, &event) != nil || method != "item/commandExecution/outputDelta" && event.Item.Type != "commandExecution" {
		return CommandObservation{}, false
	}
	return CommandObservation{Key: [3]string{event.Thread, event.Turn, cmp.Or(event.ItemID, event.Item.ID)}, Command: event.Item.Command, Delta: event.Delta, Output: event.Item.Output, Exit: event.Item.Exit}, true
}

// CommandScript is the script of a host Bash or sh command, exactly as the
// shell receives it with -c or -lc.
func CommandScript(command string) (string, bool) {
	args, ok := CommandArgs(command)
	if !ok || len(args) != 3 || args[1] != "-lc" && args[1] != "-c" {
		return "", false
	}
	name := filepath.Base(args[0])
	name = strings.TrimSuffix(name, filepath.Ext(name))
	return shellinstrumentation.ShOriginal(args[2]), name == "bash" || name == "sh" || name == "dash"
}

// CommandArgs unquotes a host command made only of literal words.
func CommandArgs(command string) ([]string, bool) {
	program, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil || len(program.Stmts) != 1 {
		return nil, false
	}
	stmt := program.Stmts[0]
	call, ok := stmt.Cmd.(*syntax.CallExpr)
	if !ok || stmt.Background || stmt.Negated || stmt.Coprocess || stmt.Disown || len(stmt.Redirs) != 0 || len(call.Assigns) != 0 || len(call.Args) < 3 {
		return nil, false
	}
	args := make([]string, 0, len(call.Args))
	for _, word := range call.Args {
		if !shellsyntax.LiteralParts(word.Parts, false) {
			return nil, false
		}
		// Fields removes shell quoting without consulting the environment or
		// executing substitutions. Reject words that expand to multiple args.
		fields, err := expand.Fields(&expand.Config{}, word)
		if err != nil || len(fields) != 1 {
			return nil, false
		}
		args = append(args, fields[0])
	}
	return args, true
}

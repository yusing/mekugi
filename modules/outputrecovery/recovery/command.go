package recovery

import (
	"fmt"

	"github.com/yusing/mekugi/contracts/command"
)

var Command = command.Definition[*Service]{Name: "mread", Description: "Continue omitted retained output without rerunning its producer. Only emitted references recover retained output; ordinary exec_command truncation has no mread recovery. Usage: `mread REF [REF ...] [--stdout|--stderr] [--max-tokens N]`. REF is a returned handle, not a path or range. Multiple handles share one budget and return one combined next_call. Failed handles report errors on stderr without discarding other handles' stdout; any failure exits nonzero. --stdout or --stderr selects one stream; otherwise both are returned. Source-row pages state their row range. Default token budget: 8000.", Run: (*Service).Execute}

func NextCall(id string, budgets ...int) string {
	command := "mread " + id
	if len(budgets) > 0 && budgets[0] > 0 {
		command += fmt.Sprintf(" --max-tokens %d", budgets[0])
	}
	return "read: incomplete; next_call: " + command + "\n"
}

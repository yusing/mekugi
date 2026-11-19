package recovery

import (
	"fmt"

	"github.com/yusing/mekugi/contracts/command"
)

var Command = command.Definition[*Service]{Name: "mread", Run: (*Service).Execute}

func NextCall(id string, budgets ...int) string {
	command := "mread " + id
	if len(budgets) > 0 && budgets[0] > 0 {
		command += fmt.Sprintf(" --max-tokens %d", budgets[0])
	}
	return "read: incomplete; next_call: " + command + "\n"
}

package retained

import (
	"strconv"
	"strings"

	"github.com/yusing/mekugi/libraries/legacycall"
)

func outcomeText(o legacycall.Outcome) string {
	label := "exec"
	if len(o.Labels) != 0 {
		label += " " + strings.Join(o.Labels, ", ")
	}
	parts := []string{label}
	if o.Status == "completed" || o.Status == "failed" {
		parts = append(parts, "command "+o.Status)
	}
	if o.Exit != nil {
		parts = append(parts, "exit "+strconv.Itoa(*o.Exit))
	}
	parts = append(parts, o.Class+", "+o.Coverage+" coverage")
	if o.Unswept != "" {
		parts = append(parts, o.Unswept)
	}
	return strings.Join(parts, " · ")
}

func hostResultText(r legacycall.HostResult) string {
	text := "host tool " + r.CallID + " " + r.Tool + ": " + r.Status
	if r.ExitCode != nil {
		text += "; exit " + strconv.Itoa(*r.ExitCode)
	}
	return text
}

package sessionpresentation

// Source: apps/mekugi/internal/router/app_server_progress.go:23:26,58:102@7166de95.
import (
	"github.com/yusing/mekugi/contracts/observation"
)

// Main progress consumes Activity's authoritative retained item evidence. The
// application adapts host facts; session presentation owns precedence and keys.
func (m *Model) ObserveActivityProgress(p observation.Event, main, selected string, source *ActivitySource) {
	method := string(p.Kind)
	if method == "item/commandExecution/terminalInteraction" {
		m.TerminalInteraction(p.ThreadID, p.TurnID, selected, p.ProcessID, p.Stdin)
		return
	}
	completed := false
	if p.ThreadID == main && method == "item/started" && p.Item.Type == "contextCompaction" {
		for _, entry := range source.Model.Entries {
			native := entry.Source.Native
			if native != nil && native.Thread == p.ThreadID && native.Turn == p.TurnID && native.Item == p.Item.ID && native.Phase == "item/completed" {
				completed = true
				break
			}
		}
	}
	m.ObserveProgress(p, selected, completed)
}

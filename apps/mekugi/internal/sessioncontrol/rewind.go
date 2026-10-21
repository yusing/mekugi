package sessioncontrol

import "github.com/yusing/mekugi/modules/sessionpreferences/preferences"

// RewindOperation waits for the exact native-shell reversal before forking.
// Request acceptance, terminal execution and turn completion are separate gates.
type RewindOperation struct {
	Thread, Turn, BeforeTurn, Provider, Command, Item string
	Approvals, Sent, Completed, Failed                bool
	Settings                                          preferences.Applied
}

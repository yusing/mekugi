package sessioncontrol

// RewindOperation waits for the exact native-shell reversal before rewinding.
// Request acceptance, terminal execution and turn completion are separate gates.
type RewindOperation struct {
	Thread, Turn, BeforeTurn, Command, Item string
	Sent, Completed, Failed                 bool
}

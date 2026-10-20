// Package guidance defines command-owned supplements for the guidance host.
package guidance

// Section accompanies its selected command. The command's description remains
// the authoritative command contract; Text supplies additional workflow advice.
type Section struct {
	Command string
	Text    string
}

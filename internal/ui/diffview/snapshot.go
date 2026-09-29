package diffview

import (
	"github.com/yusing/mekugi"
)

// Preview state is router-lifetime only and never enters the replay store.
type Preview struct {
	ID        string
	Workspace string
	Caller    string
	Turn      string `json:",omitzero"`
	Thread    string
	Files     []mekugi.ReviewFile
	Input     string // Display-only text, never executed.
	Truncated bool
	Evaluated bool `json:",omitzero"`
	Complete  bool `json:",omitzero"`
	DiffText  bool `json:",omitzero"`
	Status    string
	Footer    string `json:",omitempty"`
	Tool      string `json:",omitempty"` // The host tool whose input is predicted.
}

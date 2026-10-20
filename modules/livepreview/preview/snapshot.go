package preview

import (
	"github.com/yusing/mekugi/libraries/diffmodel"
)

// Preview state is attachment-local and never enters the replay store.
type Preview struct {
	ID          string
	Workspace   string
	Caller      string
	Turn        string `json:",omitzero"`
	Thread      string
	HostItems   []string `json:",omitempty"` // Exact command items whose observed writer effects completed.
	EditReceipt string   `json:",omitempty"` // Captured operation rows, independent of preview clipping.
	Files       []diffmodel.ReviewFile
	Input       string // Display-only text, never executed.
	Truncated   bool
	Evaluated   bool `json:",omitzero"`
	Complete    bool `json:",omitzero"`
	DiffText    bool `json:",omitzero"`
	Status      string
	Footer      string `json:",omitempty"`
	Tool        string `json:",omitempty"` // The evidenced editing tool, not its host executor.
}

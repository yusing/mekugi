package observation

// SourceAcquisition is a frozen native reader receipt. Bodies remain in the
// context response envelope; presentation consumes only independent outcomes.
type SourceAcquisition struct {
	ID      string                    `json:"id"`
	Purpose string                    `json:"purpose"`
	Sources []SourceAcquisitionResult `json:"sources"`
}

type SourceAcquisitionResult struct {
	Reference  string   `json:"reference"`
	Kind       string   `json:"kind"`
	Path       string   `json:"path,omitzero"`
	Name       string   `json:"name,omitzero"`
	Selection  []string `json:"selection,omitzero"`
	Success    bool     `json:"success"`
	Diagnostic string   `json:"diagnostic,omitzero"`
}

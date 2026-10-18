package launch

type storageInspection struct {
	Schema       string `json:"schema"`
	Target       string `json:"target"`
	Bytes        int    `json:"bytes"`
	SHA256       string `json:"sha256"`
	Dependencies int    `json:"dependencies"`
	Field        string `json:"field,omitempty"`
	Offset       int    `json:"offset,omitzero"`
	NextOffset   *int   `json:"next_offset,omitempty"`
	Text         string `json:"text,omitempty"`
}

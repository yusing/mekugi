package guidance

// Fragment binds exact instruction bytes to an authenticated host content part.
type Fragment struct {
	Thread string `json:"thread"`
	Item   string `json:"item"`
	Index  int    `json:"index"`
	Kind   string `json:"kind"`
	Text   string `json:"text"`
}

// Replacement binds one prepared projection to its immutable native source.
type Replacement struct {
	Fragment Fragment
	Text     string
}

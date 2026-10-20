package diffmodel

// Counts reports known added and removed rows; negative values mean unavailable.
type Counts struct {
	Added, Removed int
}

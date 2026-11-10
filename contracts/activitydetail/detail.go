// Package activitydetail defines renderer-neutral Activity identities and detail pages.
package activitydetail

type Operation struct {
	Thread, Item string
}

type Request struct {
	Operation
	Path      string
	Directory bool
}

type Page struct {
	ID, Verb, Path, DisplayPath, Code, Language string
}

type Result struct {
	Pages    []Page
	Selected string
}

// SourceAnchor identifies the last observed writer boundary in a captured window.
// Segment is one-based within Call. It is not a claim of per-file attribution.
type SourceAnchor struct {
	Call    string
	Segment int
}

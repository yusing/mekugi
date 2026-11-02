// Package activitydetail defines optional renderer-neutral Activity detail pages.
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

// Package activitydetail defines optional renderer-neutral Activity detail pages.
package activitydetail

import "github.com/yusing/mekugi/contracts/scope"

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

type Provider struct {
	Name    string
	Resolve func(*scope.Store, Request) Result
}

type Definition[S any] struct {
	Name    string
	Resolve func(S, *scope.Store, Request) Result
}

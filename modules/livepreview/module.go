package livepreview

import (
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/livepreview/preview"
)

type Deps struct{}

var Module = module.Descriptor[Deps, *preview.Service, module.NoBindings]{
	ID: "livepreview", Summary: "Project provisional source changes and publish bounded live view state",
	Lifetime: module.AttachedSession,
	New:      func(Deps, module.Slots) (*preview.Service, error) { return new(preview.Service), nil },
}

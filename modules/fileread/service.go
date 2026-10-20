package fileread

import (
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/fileread/read"
)

type Deps struct{}

func newService(Deps, module.Slots) (read.Service, error) { return read.Service{}, nil }

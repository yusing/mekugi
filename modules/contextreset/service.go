package contextreset

import (
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/contextreset/guidance"
	"github.com/yusing/mekugi/modules/fileread/read"
	"github.com/yusing/mekugi/modules/sourceoutline/outline"
)

type Deps struct {
	Reader    read.Service
	Outline   outline.Service
	ReadSkill guidance.ReadSkill
}

func newService(deps Deps, _ module.Slots) (guidance.Service, error) {
	return guidance.New(deps.Reader, deps.Outline, deps.ReadSkill)
}

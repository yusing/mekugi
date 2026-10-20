package guidance

import (
	"context"
	"errors"

	"github.com/yusing/mekugi/libraries/commandanalysis"
	"github.com/yusing/mekugi/modules/fileread/read"
	"github.com/yusing/mekugi/modules/sourceoutline/outline"
)

// ReadSkill supplies current content and whether its source is manager-owned.
// The acquisition owner retains its invocation environment and authorization.
type ReadSkill func(ctx context.Context, workspace, name, path string) ([]byte, bool, error)

type Service struct {
	reader    read.Service
	outline   outline.Service
	readSkill ReadSkill
}

func New(reader read.Service, outline outline.Service, readSkill ReadSkill) (Service, error) {
	if readSkill == nil {
		return Service{}, errors.New("instruction loading requires skill acquisition")
	}
	return Service{reader: reader, outline: outline, readSkill: readSkill}, nil
}

// Read carries completed-call provenance, never execution authority.
type Read struct {
	Tool, Name       string
	ResponseRound    int
	Restored         bool
	Commands         []commandanalysis.Command
	Skills           []Skill
	HasSkillIdentity bool
}

type SelectedSkill struct {
	Skill      Skill
	Tool, Name string
}

type Input struct {
	Workspace      string
	Instructions   []Instruction
	SelectedSkills []SelectedSkill
	CompletedReads []Read
	FirstReadOrder []string
}

package policy

import (
	"fmt"
	"strings"

	"github.com/yusing/mekugi/contracts/guidance"
	"github.com/yusing/mekugi/modules/agentguidance/contextnotice"
	"github.com/yusing/mekugi/modules/agentguidance/environment"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

type Service struct {
	Changes           changes.Service
	History           *history.Service
	Environment       environment.Store
	ContextNotices    contextnotice.Store
	ChildReplySummary bool
	Sections          map[string]string
	Conflicts         func(string) string
}

func New(sections []guidance.Section) (*Service, error) {
	service := &Service{Sections: make(map[string]string), Conflicts: newConflictRewriter()}
	for _, section := range sections {
		if section.Command == "" || strings.TrimSpace(section.Text) == "" {
			return nil, fmt.Errorf("guidance requires a command and text")
		}
		if _, exists := service.Sections[section.Command]; exists {
			return nil, fmt.Errorf("duplicate guidance for %s", section.Command)
		}
		service.Sections[section.Command] = strings.TrimSpace(section.Text)
	}
	return service, nil
}

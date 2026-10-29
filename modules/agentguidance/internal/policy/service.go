package policy

import (
	"fmt"
	"strings"

	"github.com/yusing/mekugi/contracts/guidance"
	"github.com/yusing/mekugi/modules/agentguidance/environment"
)

type Service struct {
	Environment       environment.Store
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

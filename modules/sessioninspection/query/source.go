package query

import (
	"context"
	"errors"

	"github.com/yusing/mekugi/contracts/inspection"
	"github.com/yusing/mekugi/libraries/legacycall"
)

// CallEvidence interprets immutable host evidence at the backend boundary.
// Query logic sees decisions, never protocol envelopes or execution handles.
type CallEvidence interface {
	Verify(legacycall.History) error
	EmptyPoll(*legacycall.History) bool
	Commands() []string
}

type Call struct {
	ID, Tool, Input, Output, Workspace, Timestamp, Model string
	Line                                                 int
	HasOutput, ReadContinuation                          bool
	Evidence                                             CallEvidence
}

type Rollout struct {
	ThreadID, Cwd, Origin string
	Models                []string
	Calls                 []Call
}

type Source func(context.Context, string, func(inspection.Event) error) (Rollout, error)

type Service struct {
	source        Source
	contributions inspection.Contribution
}

func New(contributions []inspection.Contribution) (*Service, error) {
	s := new(Service)
	for _, c := range contributions {
		if c.NewAX == nil && c.ProviderUsage == nil {
			return nil, errors.New("empty inspection contribution")
		}
		if c.NewAX != nil {
			if s.contributions.NewAX != nil {
				return nil, errors.New("duplicate AX inspection contribution")
			}
			s.contributions.NewAX = c.NewAX
		}
		if c.ProviderUsage != nil {
			if s.contributions.ProviderUsage != nil {
				return nil, errors.New("duplicate provider usage inspection contribution")
			}
			s.contributions.ProviderUsage = c.ProviderUsage
		}
	}
	return s, nil
}

func (s *Service) BindSource(source Source) error {
	if source == nil {
		return errors.New("missing inspection source")
	}
	if s.source != nil {
		return errors.New("inspection source is already bound")
	}
	s.source = source
	return nil
}

func (s *Service) HasAX() bool            { return s != nil && s.contributions.NewAX != nil }
func (s *Service) HasProviderUsage() bool { return s != nil && s.contributions.ProviderUsage != nil }

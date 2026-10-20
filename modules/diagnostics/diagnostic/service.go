package diagnostic

import (
	"errors"

	"github.com/yusing/mekugi/contracts/diagnosticsection"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	"github.com/yusing/mekugi/modules/sessioninspection/query"
)

type Service struct {
	history    *history.Service
	inspection *query.Service
	sections   diagnosticsection.Contribution
}

func New(history *history.Service, inspection *query.Service, contributions []diagnosticsection.Contribution) (*Service, error) {
	if history == nil || inspection == nil {
		return nil, errors.New("diagnostics requires history and session inspection")
	}
	s := &Service{history: history, inspection: inspection}
	for _, c := range contributions {
		if c.Transport == nil && c.PrepareReads == nil && c.ReadSnapshot == nil {
			return nil, errors.New("empty diagnostic contribution")
		}
		if (c.PrepareReads == nil) != (c.ReadSnapshot == nil) {
			return nil, errors.New("incomplete AX diagnostic contribution")
		}
		if c.Transport != nil {
			if c.Transport.Record == nil || c.Transport.Metrics == nil {
				return nil, errors.New("incomplete transport diagnostic contribution")
			}
			if s.sections.Transport != nil {
				return nil, errors.New("duplicate transport diagnostic contribution")
			}
			s.sections.Transport = c.Transport
		}
		if c.PrepareReads != nil {
			if s.sections.PrepareReads != nil {
				return nil, errors.New("duplicate AX diagnostic contribution")
			}
			s.sections.PrepareReads, s.sections.ReadSnapshot = c.PrepareReads, c.ReadSnapshot
		}
	}
	return s, nil
}

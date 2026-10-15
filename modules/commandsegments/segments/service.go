package segments

import (
	"context"

	"github.com/yusing/mekugi/modules/commandsegments/reports"
	"github.com/yusing/mekugi/modules/commandsegments/transport"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

// Service combines retained report operations with optional invocation transport.
// Live and LiveError are settled by Start before the application receives it.
type Service struct {
	Reports   *reports.Service
	Live      *transport.Server
	LiveError error
	enabled   bool
}

func New(history *history.Service, enabled bool) (*Service, error) {
	reports, err := reports.New(history)
	if err != nil {
		return nil, err
	}
	return &Service{Reports: reports, enabled: enabled}, nil
}

func (s *Service) Start(ctx context.Context) error {
	if !s.enabled {
		return nil
	}
	s.Live, s.LiveError = transport.Listen(ctx)
	// Transport is auxiliary. The application reports its absence independently
	// of approval startup; retained reports remain available.
	return ctx.Err()
}

func (s *Service) Stop(context.Context) error { s.Live.Close(); return nil }

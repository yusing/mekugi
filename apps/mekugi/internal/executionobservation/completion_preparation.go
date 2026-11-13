package executionobservation

import (
	"errors"

	"github.com/yusing/mekugi/modules/agentguidance/compact"
	"github.com/yusing/mekugi/modules/outputrecovery/recovery"
)

func (p *Preparer) completionReader(thread string) (compact.ChangeReader, *recovery.Session, error) {
	if p.Retained.Store == nil || p.Recovery == nil {
		return compact.ChangeReader{}, nil, errors.New("completion attachment storage is unavailable")
	}
	selected := p.Retained
	selected.Thread = thread
	admitted, err := selected.Store.ReadAdmittedScope(thread)
	if err != nil {
		return compact.ChangeReader{}, nil, err
	}
	selected.Namespace = admitted.Namespace
	store, err := p.Recovery.AttachStore(selected.Store, selected.Namespace, thread, recovery.DefaultLimits(), selected.Writes)
	return compact.ChangeReader{Service: p.Changes, Scope: selected}, store, err
}

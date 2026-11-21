package executionobservation

import (
	"errors"

	"github.com/yusing/mekugi/modules/agentguidance/compact"
)

func (p *Preparer) completionReader(thread string) (compact.ChangeReader, error) {
	if p.Retained.Store == nil {
		return compact.ChangeReader{}, errors.New("completion attachment storage is unavailable")
	}
	selected := p.Retained
	selected.Thread = thread
	admitted, err := selected.Store.ReadAdmittedScope(thread)
	if err != nil {
		return compact.ChangeReader{}, err
	}
	selected.Namespace = admitted.Namespace
	return compact.ChangeReader{Service: p.Changes, Scope: selected}, nil
}

package binding

import (
	"errors"

	"github.com/yusing/mekugi/contracts/observation"
	"github.com/yusing/mekugi/contracts/scope"
)

// Observation accesses transcript correlation in application-owned scopes.
// Its callbacks consume public facts and cannot issue host operations.
type Observation struct {
	NewTranscript func(*scope.Store) observation.Transcript
}

func (r *Registry) Observation(observation Observation) error {
	if observation.NewTranscript == nil {
		return errors.New("observation requires a transcript factory")
	}
	if r.observations != nil {
		return errors.New("duplicate observation registration")
	}
	r.observations = &observation
	return nil
}

func (r *Registry) RegisteredObservation() *Observation { return r.observations }

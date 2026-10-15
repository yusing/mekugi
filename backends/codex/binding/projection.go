package binding

import (
	"errors"
	"fmt"
	"slices"
)

// InputProjection prepares once against original input before application
// additions. Its returned function applies once, after all evidence consumers.
// Only the returned function may hold request-local state.
type InputProjection struct {
	Name    string
	Prepare func([]byte) PreparedInputProjection
}

type PreparedInputProjection func([]byte) ProjectionResult

type ProjectionResult struct {
	Input   []byte
	Receipt *ProjectionReceipt
}

// ProjectionReceipt describes repeated text in this request copy, not host
// completion or provider billing. Call keys keep the original Codex identity.
type ProjectionReceipt struct {
	OutputRepetitions uint64
	RepeatedCalls     map[string]string
}

func (r *Registry) InputProjection(projection InputProjection) error {
	if projection.Name == "" || projection.Prepare == nil {
		return errors.New("input projection requires a name and prepare function")
	}
	if slices.ContainsFunc(r.projections, func(old InputProjection) bool { return old.Name == projection.Name }) {
		return fmt.Errorf("duplicate input projection %q", projection.Name)
	}
	r.projections = append(r.projections, projection)
	return nil
}

func (r *Registry) InputProjections() []InputProjection { return slices.Clone(r.projections) }

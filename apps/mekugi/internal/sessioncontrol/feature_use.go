package sessioncontrol

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"sync/atomic"

	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

// NativeFeatureUse borrows the root connection for one launch, including Claude
// and retired descendants. Only transport availability lives here, never counts
// or receipts. A failed fact leaves measurement unavailable for the launch.
type NativeFeatureUse struct {
	requests featureUseRequests
	failure  atomic.Pointer[error]
}

type featureUseRequests interface {
	recordFeature(context.Context, control.FeatureFact) error
	readFeatureUse(context.Context, []string) (map[string]control.FeatureUse, error)
}

func NewNativeFeatureUse(requests featureUseRequests) *NativeFeatureUse {
	return &NativeFeatureUse{requests: requests}
}

func (f *NativeFeatureUse) RecordFeature(ctx context.Context, fact control.FeatureFact) {
	if f == nil || f.failure.Load() != nil {
		return
	}
	if err := f.requests.recordFeature(ctx, fact); err != nil {
		f.InvalidateFeatureUse(err)
	}
}

func (f *NativeFeatureUse) InvalidateFeatureUse(err error) {
	if f != nil && err != nil {
		f.failure.CompareAndSwap(nil, &err)
	}
}

func (f *NativeFeatureUse) ReadFeatureUse(ctx context.Context, threads []string) (map[string]control.FeatureUse, error) {
	if f == nil {
		return nil, errors.New("native feature measurement is unavailable")
	}
	if failure := f.failure.Load(); failure != nil {
		return nil, *failure
	}
	counts, err := f.requests.readFeatureUse(ctx, threads)
	// A concurrent failed delivery must not turn a partial snapshot into a complete total.
	if failure := f.failure.Load(); failure != nil {
		return nil, *failure
	}
	return counts, err
}

func (r *Requests[T]) recordFeature(ctx context.Context, fact control.FeatureFact) error {
	_, err := r.call(ctx, fact.Thread, "featureUse/record", fact, nil)
	return err
}

func (r *Requests[T]) readFeatureUse(ctx context.Context, threads []string) (map[string]control.FeatureUse, error) {
	if len(threads) == 0 || threads[0] == "" {
		return nil, errors.New("feature measurement requires an actor")
	}
	raw, err := r.call(ctx, threads[0], "featureUse/read", struct {
		Threads []string `json:"threads"`
	}{threads}, nil)
	if err != nil {
		return nil, err
	}
	var response struct {
		Threads map[string]control.FeatureUse `json:"threads"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, err
	}
	return response.Threads, nil
}

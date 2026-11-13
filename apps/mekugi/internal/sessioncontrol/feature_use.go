package sessioncontrol

import (
	"context"
	json "encoding/json/v2"
	"errors"

	"github.com/yusing/mekugi/backends/codex/appserver"
)

// NativeFeatureUse borrows the root connection for on-demand launch snapshots,
// including retired descendants. Counts and receipts stay native.
type NativeFeatureUse struct {
	requests featureUseRequests
}

type featureUseRequests interface {
	readFeatureUse(context.Context, []string) (map[string]appserver.FeatureUse, error)
}

func NewNativeFeatureUse(requests featureUseRequests) *NativeFeatureUse {
	return &NativeFeatureUse{requests: requests}
}

func (f *NativeFeatureUse) ReadFeatureUse(ctx context.Context, threads []string) (map[string]appserver.FeatureUse, error) {
	if f == nil {
		return nil, errors.New("native feature measurement is unavailable")
	}
	return f.requests.readFeatureUse(ctx, threads)
}

func (r *Requests[T]) readFeatureUse(ctx context.Context, threads []string) (map[string]appserver.FeatureUse, error) {
	if len(threads) == 0 || threads[0] == "" {
		return nil, errors.New("feature measurement requires an actor")
	}
	raw, err := r.call(ctx, threads[0], "featureUse/read", struct {
		Threads []string `json:"threads"`
	}{threads})
	if err != nil {
		return nil, err
	}
	var response struct {
		Threads map[string]appserver.FeatureUse `json:"threads"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, err
	}
	return response.Threads, nil
}

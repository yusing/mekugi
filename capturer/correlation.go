package capturer

import "context"

// RequestCorrelation exposes only the immutable local exchange identity. No
// credentials or caller headers are returned, and no transport header is added.
func RequestCorrelation(ctx context.Context) (captureID string, requestSequence uint64) {
	state, _ := ctx.Value(captureKey{}).(*requestState)
	if state == nil {
		return "", 0
	}
	return state.captureID, state.sequence
}

package router

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"time"
)

// Local journal tools need a model continuation without inventing a client
// executor call. Each completed journal result is also retained by Codex; its
// original router call is restored from durable replay on subsequent requests.
type journalContinuationKey struct{}
type journalContinuation struct {
	depth        int
	clientOutput []map[string]json.RawMessage
}

func nextJournalRequest(original map[string]json.RawMessage, transform *mekugiResponseTransform) (parsedResponsesRequest, error) {
	fields := maps.Clone(original)
	var input []map[string]json.RawMessage
	if raw := fields["input"]; len(raw) != 0 {
		if err := json.Unmarshal(raw, &input); err != nil {
			return parsedResponsesRequest{}, err
		}
	}
	fields["input"] = mustMarshalJSON(append(input, transform.journalOutputItems()...))
	delete(fields, "previous_response_id")
	return parseResponsesRequest(mustMarshalJSON(fields))
}

func continueJournalContext(ctx context.Context, transform *mekugiResponseTransform) (context.Context, error) {
	state, _ := ctx.Value(journalContinuationKey{}).(journalContinuation)
	if state.depth >= maxJournalItems {
		return nil, errors.New("journal tool continuation limit reached")
	}
	state.depth++
	state.clientOutput = append(state.clientOutput, transform.journalClientOutput...)
	return context.WithValue(ctx, journalContinuationKey{}, state), nil
}

func resetJournalExchange(provider responseProvider) {
	if exchange, ok := provider.(*webSocketExchange); ok {
		exchange.observation.Finish(nil)
		exchange.ended = false
		exchange.first = nil
		exchange.buffer.Reset()
		exchange.automatic = false
	}
}

func journalRequestWindow(ctx context.Context) time.Duration {
	if deadline, ok := ctx.Deadline(); ok {
		return max(time.Millisecond, time.Until(deadline))
	}
	return defaultRequestTimeout
}

package sessioncontrol

import (
	"context"
	json "encoding/json/v2"
	"errors"

	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

// SideQuestion uses Claude's native side-question control request, never primary input.
func (r *ClaudeRuntime) SideQuestion(ctx context.Context, target *control.Member, text string) (string, error) {
	r.mu.Lock()
	err := r.identity(target.Thread)
	if err == nil && r.ledger.Info.Session != target.Session {
		err = errors.New("Claude side-question session changed")
	}
	r.mu.Unlock()
	if err != nil {
		return "", err
	}
	params, err := json.Marshal(map[string]string{"question": text})
	if err != nil {
		return "", err
	}
	raw, err := r.native.Request(ctx, "side_question", params)
	if err != nil {
		return "", err
	}
	var answer string
	return answer, json.Unmarshal(raw, &answer)
}

package launch

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"os/exec"

	"github.com/yusing/mekugi/backends/claude"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

// DiscoverModels borrows the caller's admitted workspace and captured environment
// for one initialization-only runtime, without creating a logical member.
func (e *threadExecution) DiscoverModels(ctx context.Context, caller control.Identity) (models []control.ModelOption, discoveryErr error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if e == nil || e.shared == nil || e.setup == nil {
		return nil, errors.New("Claude model discovery requires captured invocation owners")
	}
	member, err := e.shared.Caller(caller)
	if err != nil {
		return nil, err
	}
	bun, err := exec.LookPath("bun")
	if err != nil {
		return nil, fmt.Errorf("locate Claude SDK Bun runtime: %w", err)
	}
	nativeExecutable, err := exec.LookPath("claude")
	if err != nil {
		return nil, fmt.Errorf("locate Claude executable: %w", err)
	}
	client, err := claude.Start(ctx, claude.Config{
		BunPath: bun, ClaudePath: nativeExecutable, Workspace: member.Settings.Workspace,
		Env: e.setup.environment, Options: jsontext.Value(`{"tools":[],"persistSession":false}`),
		OnRequest: func(_ context.Context, method string, _ jsontext.Value) (jsontext.Value, error) {
			return nil, fmt.Errorf("Claude model discovery rejects runtime effect %q", method)
		},
	})
	if err != nil {
		return nil, err
	}
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for range client.Events() {
		}
	}()
	defer func() {
		discoveryErr = errors.Join(discoveryErr, client.Close(), ctx.Err())
		<-drained
	}()
	nativeModels, err := client.Models()
	if err != nil {
		return nil, err
	}
	models = make([]control.ModelOption, 0, len(nativeModels))
	for _, model := range nativeModels {
		option := control.ModelOption{ID: "claude:" + model.Value, Name: model.DisplayName, Description: model.Description}
		if model.ResolvedModel != "" {
			option.ResolvedModel = "claude:" + model.ResolvedModel
		}
		models = append(models, option)
	}
	return models, nil
}

package launch

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"testing"
	"testing/synctest"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	threadbinding "github.com/yusing/mekugi/modules/threadcontrol/codexbinding"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

type claudeLifecycleParent struct {
	control.Runtime
	inputs []control.NativeInput
}

func (p *claudeLifecycleParent) Input(_ context.Context, input control.NativeInput) (string, error) {
	if input.Thread != "parent" || input.Run {
		return "", errors.New("unexpected parent dispatch")
	}
	p.inputs = append(p.inputs, input)
	return "", nil
}

type claudeLifecycleHost struct {
	claudeHookHost
	parent *claudeLifecycleParent
}

type claudeLifecycleRuntime struct {
	*sessioncontrol.ClaudeRuntime
	admitted    <-chan struct{}
	acquired    chan struct{}
	beforeInput func() error
}

func (r *claudeLifecycleRuntime) AdmitControl(ctx context.Context) (func() error, error) {
	release, err := r.ClaudeRuntime.AdmitControl(ctx)
	if err == nil && r.admitted != nil {
		close(r.acquired)
		<-r.admitted
	}
	return release, err
}

func (r *claudeLifecycleRuntime) Input(ctx context.Context, input control.NativeInput) (string, error) {
	if r.beforeInput != nil {
		if err := r.beforeInput(); err != nil {
			return "", err
		}
	}
	return r.ClaudeRuntime.Input(ctx, input)
}

func (h claudeLifecycleHost) Runtime(thread string) (control.Runtime, error) {
	if thread == "parent" {
		return h.parent, nil
	}
	return h.runtime, nil
}

func (h claudeLifecycleHost) Resume(_ context.Context, thread string) (control.Runtime, error) {
	return h.Runtime(thread)
}

func newClaudeLifecycleFixture(t *testing.T) (*claudeHookFixture, *claudeLifecycleParent, *claudeLifecycleRuntime) {
	t.Helper()
	f := newClaudeHookFixture(t)
	b := f.bridge
	parent := &claudeLifecycleParent{}
	runtime := &claudeLifecycleRuntime{ClaudeRuntime: b.runtime}
	driver, err := threadbinding.NewDriver(b.execution.shared)
	if err != nil {
		t.Fatal(err)
	}
	audience := sessioncontrol.NewAudience(nil)
	t.Cleanup(audience.Close)
	if err := driver.Connect(claudeLifecycleHost{runtime: runtime, parent: parent}, audience); err != nil {
		t.Fatal(err)
	}
	if err := driver.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := driver.Stop(context.WithoutCancel(t.Context())); err != nil {
			t.Error(err)
		}
	})
	b.execution.driver = driver
	return f, parent, runtime
}

func TestClaudeLaunchFollowupWaitsForTerminalReduction(t *testing.T) {
	for _, reason := range []string{"completed", "aborted_streaming", "api_error"} {
		t.Run(reason, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f, parent, runtime := newClaudeLifecycleFixture(t)
				b := f.bridge
				driver := b.execution.driver
				admitted := make(chan struct{})
				runtime.admitted, runtime.acquired = admitted, make(chan struct{})
				previous := f.inputs[0]
				before, _ := b.execution.shared.Member("child")
				raw, _ := json.Marshal(map[string]any{"type": "result", "subtype": "success", "session_id": b.info.Session, "uuid": "old-native-result", "user_message_uuid": previous.UUID, "terminal_reason": reason, "result": "previous answer"})
				terminal, err := b.runtime.HandleEvent(t.Context(), raw)
				if err != nil || !terminal.Completed {
					t.Fatalf("previous native terminal: %+v %v", terminal, err)
				}
				// Pause the bridge between native observation and logical reduction.
				// The actual coordination operation must wait before reserving work.
				done := make(chan error, 1)
				go func() {
					_, err := driver.Call(t.Context(), control.Identity{Thread: "parent", Session: "parent-session", Turn: "parent-turn", Call: "boundary-followup", Item: "boundary-followup"}, "followup_task", jsontext.Value(`{"target":"agent:main/child","message":"next work"}`))
					done <- err
				}()
				synctest.Wait()
				select {
				case <-runtime.acquired:
					t.Fatal("followup acquired logical admission before preceding completion")
				default:
				}
				if len(f.inputs) != 1 {
					t.Fatal("followup dispatched before preceding logical completion")
				}
				if err := b.reduceEvent(t.Context(), terminal); err != nil {
					t.Fatal(err)
				}
				synctest.Wait()
				// Keep filesystem flock retries out of virtual-time contention:
				// finish old-result delivery before the newly admitted dispatch.
				close(admitted)
				synctest.Wait()
				if err := <-done; err != nil {
					t.Fatalf("boundary followup through Thread Control failed: %v", err)
				}
				member, _ := b.execution.shared.Member("child")
				if len(f.inputs) != 2 || f.inputs[1].Priority != "" || !member.HostRunning || member.Turn == before.Turn || member.Generation != before.Generation+1 || member.Turn != b.runtime.CurrentTurn() {
					t.Fatalf("followup did not start its newly admitted turn: inputs=%+v member=%+v", f.inputs, member)
				}
				if len(parent.inputs) != 1 || parent.inputs[0].Outcome != terminal.Status || parent.inputs[0].SourceGeneration != before.Generation {
					t.Fatalf("previous outcome lost its ordered attribution: %+v", parent.inputs)
				}
				if failures := driver.Failures(); len(failures) != 0 {
					t.Fatalf("terminal lifecycle failed: %v", failures)
				}
			})
		})
	}
}

func TestClaudeLaunchAdmittedFollowupKeepsTurnAcrossNativeTerminal(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f, parent, runtime := newClaudeLifecycleFixture(t)
		b := f.bridge
		before, _ := b.execution.shared.Member("child")
		previous := f.inputs[0]
		raw, _ := json.Marshal(map[string]any{"type": "result", "subtype": "success", "session_id": b.info.Session, "uuid": "old-native-result", "user_message_uuid": previous.UUID, "terminal_reason": "completed", "result": "previous answer"})
		runtime.beforeInput = func() error {
			if err := b.event(t.Context(), raw); err != nil {
				return err
			}
			synctest.Wait()
			return nil
		}
		_, err := b.execution.driver.Call(t.Context(), control.Identity{Thread: "parent", Session: "parent-session", Turn: "parent-turn", Call: "admitted-followup", Item: "admitted-followup"}, "followup_task", jsontext.Value(`{"target":"agent:main/child","message":"next work"}`))
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		member, _ := b.execution.shared.Member("child")
		if len(f.inputs) != 2 || f.inputs[1].Priority != "now" || !member.HostRunning || member.Turn != before.Turn || member.Generation != before.Generation || len(parent.inputs) != 0 {
			t.Fatalf("native terminal displaced admitted steering: inputs=%+v member=%+v outcomes=%+v", f.inputs, member, parent.inputs)
		}
		raw, _ = json.Marshal(map[string]any{"type": "result", "subtype": "success", "session_id": b.info.Session, "uuid": "followup-native-result", "user_message_uuid": f.inputs[1].UUID, "terminal_reason": "completed", "result": "followup answer"})
		if err := b.event(t.Context(), raw); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		member, _ = b.execution.shared.Member("child")
		if member.HostRunning || member.Status != control.Completed || b.runtime.CurrentTurn() != "" || len(parent.inputs) != 1 || parent.inputs[0].Message != "followup answer" {
			t.Fatalf("followup terminal did not settle once: member=%+v outcomes=%+v", member, parent.inputs)
		}
		if failures := b.execution.driver.Failures(); len(failures) != 0 {
			t.Fatalf("terminal lifecycle failed: %v", failures)
		}
	})
}

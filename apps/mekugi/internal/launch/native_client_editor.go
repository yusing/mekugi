package launch

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
	"github.com/yusing/mekugi/modules/conversation/composer"
	conversation "github.com/yusing/mekugi/modules/conversation/presentation"
)

type nativeEditorHandoff struct {
	path       string
	editor     *composer.Editor
	target     conversation.EditorTarget
	attachment uint64
}

func (c *nativeClient) retainsEditor(editor *composer.Editor) bool {
	for _, current := range c.editors {
		if current == editor {
			return true
		}
	}
	return false
}

func (c *nativeClient) accept(event sessionapi.NativeEvent) {
	previous := c.event.Frame.View.Main.ID
	if c.event.Attachment != event.Attachment || c.event.Frame.View.Version.Attachment != event.Frame.View.Version.Attachment {
		c.selection = nil
		c.closeLocalDetails()
		// Presentation versions are runtime-local. A new stream can reuse the
		// same numeric attachment and prompt IDs without inheriting their state.
		c.reconcilePromptEditors(&sessionapi.NativeFrame{})
		c.prompts = nativeClientPrompts{}
		c.secretKey, c.secrets = "", nil
		*c.renderer.terminal.Prompts = terminalsession.Prompts{}
		c.renderer.replace(event.Frame.View.Version.Attachment)
		c.streams, c.links = terminalsession.Streams{}, markdownLinkChecks{}
		c.prediction, c.predictionRevision = [3]string{}, 0
		c.completionReady, c.preferenceReady = false, false
	}
	c.reconcilePromptEditors(&event.Frame)
	c.prompts.update(&event.Frame)
	c.event = event
	if c.runtime != nil && previous == "" && event.Frame.View.Main.ID != "" && c.editors["main"] != nil {
		c.runtime.editors[[2]string{event.Frame.View.Info.Workspace, event.Frame.View.Main.ID}] = c.editors["main"]
	}
	if call := c.request(nativeObservationCall); call != nil && call.operation.Output != nil {
		output := event.Frame.MainOutput
		if call.operation.Output.Agent {
			output = event.Frame.AgentOutput
		}
		call.unresolved = slices.DeleteFunc(call.unresolved, func(demand activity.OutputDemand) bool {
			return slices.ContainsFunc(output, func(result activity.OutputResult) bool { return result.Demand == demand })
		})
		call.hydrated = len(call.unresolved) == 0
		call.reacquire = call.reacquire || event.Frame.ReacquireOutput
		call.outputLost = call.outputLost || event.Frame.ReacquireOutput
	}
	if pending := c.selection; pending != nil {
		output := event.Frame.MainOutput
		if pending.selection.View == c.renderer.terminal.Agents {
			output = event.Frame.AgentOutput
		}
		if len(output) > 0 {
			pending.ready = pending.selection.CompleteCapture(output)
		}
	}
	if previous != event.Frame.View.Main.ID {
		c.streams = terminalsession.Streams{}
	}
	c.prompts.overlay(&c.event.Frame.View)
	c.refresh(time.Now())
	c.acceptFacts(previous)
	c.applyEffects()
	c.refresh(time.Now())
}

func (c *nativeClient) applyEffects() {
	defer c.stageReceipt(nil)
	effects := c.event.Frame.Effects
	if notification := c.event.Frame.Notification; notification != nil {
		effects = append(slices.Clone(effects), *notification)
	}
	var reset uint64
	if activity := c.event.Frame.Activity; activity != nil {
		reset = activity.Reset
		if reset > c.effect {
			c.streams = terminalsession.Streams{}
			c.renderer.terminal.Transcript.ResetPacing()
			c.renderer.terminal.Agents.ResetPacing()
		}
		effects = append(slices.Clone(effects), activity.Effects...)
	}
	if c.event.Frame.Notification != nil || c.event.Frame.Activity != nil {
		slices.SortFunc(effects, func(a, b sessionapi.NativeEffect) int { return cmp.Compare(a.ID, b.ID) })
	}
	for _, effect := range effects {
		if effect.ID <= c.effect {
			continue
		}
		var ready bool
		effect, ready = c.restoreEffectImages(effect)
		if !ready {
			return
		}
		editor := c.editor("main")
		if effect.Thread != "" && effect.Thread != c.event.Frame.View.Main.ID {
			editor = c.editor("side:" + effect.Thread)
		}
		if effect.Target != nil {
			// Ordinary effects retain their thread's editor after selection changes.
			target := conversation.EditorTarget{Revision: editor.Revision()}
			if effect.Target.Call != "" || effect.Target.Request != "" {
				editor, target = c.selectedEditor()
			}
			if target != *effect.Target {
				if effect.Skills == nil {
					c.notice = "The editor changed before the requested insertion completed"
				}
				c.effect = effect.ID
				continue
			}
		}
		if effect.Skills != nil {
			editor.Skills = slices.Clone(*effect.Skills)
			editor.Changed()
		}
		if effect.Action != nil {
			if _, err := editor.Apply(*effect.Action); err != nil {
				c.notice = err.Error()
			}
		}
		if effect.Restore != nil {
			editor.Restore(*effect.Restore)
		}
		if effect.ShellRestore != nil {
			main := c.editor("main")
			if main.Text == "" {
				main.Replace(*effect.ShellRestore)
			} else {
				main.Remember(*effect.ShellRestore)
				c.notice = "Shell command rejected · command saved in input history (↑)"
			}
		}
		if selection := effect.Selection; selection != nil {
			if err := editor.InsertSelection(selection.Description, selection.Text, selection.Source, selection.Pending); err != nil {
				c.notice = err.Error()
			}
		}
		if effect.Clipboard != nil {
			c.notice, _ = c.renderer.terminal.Frame.CopyText(*effect.Clipboard)
		}
		if preferences := effect.Presentation; preferences != nil {
			c.renderer.terminal.LiveHidden = preferences.LiveHidden
			c.renderer.terminal.Main.InterruptLocked = preferences.InterruptLocked
			c.renderer.terminal.Main.PredictionsDisabled = preferences.PredictionsDisabled
		}
		if len(effect.Deliveries) > 0 {
			c.streams.Deliver(effect.Deliveries, c.renderer.terminal.Transcript, c.renderer.terminal.Agents)
		}
		if notification := effect.Notification; notification != nil {
			c.notifications.notify(&c.feedback, c.renderer.terminal, nil, notification.Event, notification.Message)
		}
		if effect.Reattach != nil {
			c.reattach = *effect.Reattach
		}
		c.effect = effect.ID
	}
	// Earlier semantic effects must settle before a compacted activity watermark
	// can be acknowledged, including asynchronous image restoration.
	c.effect = max(c.effect, reset)
}

type nativeClipboardResult struct {
	stream     string
	attachment uint64
	editor     *composer.Editor
	target     conversation.EditorTarget
	path       string
	err        error
}

func (c *nativeClient) clipboard() {
	if c.clipboardPending {
		return
	}
	if c.runtime != nil {
		if c.runtime.clipboardOwner != nil {
			c.notice = "Clipboard image acquisition is still pending"
			return
		}
		c.runtime.clipboardOwner = c
	}
	editor, target := c.selectedEditor()
	result := nativeClipboardResult{stream: c.event.Attachment, attachment: c.event.Frame.View.Version.Attachment, editor: editor, target: target}
	c.clipboardPending = true
	c.workers.Go(func() {
		result.path, result.err = clipboardImage(c.ctx)
		if c.ctx.Err() != nil {
			if result.path != "" {
				_ = os.Remove(result.path)
			}
			return
		}
		select {
		case c.runtime.messages <- nativeClientMessage{owner: c, clipboard: &result}:
		case <-c.ctx.Done():
			if result.path != "" {
				_ = os.Remove(result.path)
			}
		}
	})
}

func (c *nativeClient) clipboardResult(result nativeClipboardResult) {
	c.clipboardPending = false
	if c.runtime != nil && c.runtime.clipboardOwner == c {
		c.runtime.clipboardOwner = nil
	}
	if result.err != nil {
		c.notice = result.err.Error()
		c.refresh(time.Now())
		return
	}
	editor := result.editor
	if c.event.Attachment != result.stream || !c.retainsEditor(editor) || editor.Revision() != result.target.Revision || c.event.Frame.View.Version.Attachment != result.attachment {
		_ = os.Remove(result.path)
		c.notice = "The editor changed before the clipboard image arrived"
	} else {
		c.ownedImages[result.path] = true
		editor.InsertImage(result.path)
		c.notice = ""
	}
	c.refresh(time.Now())
}

// external runs only after WithRawPane has released input and restored terminal
// modes. Publications continue to drain while the editor owns the terminal.
func (c *nativeClient) external(ctx context.Context, stdin, stdout *os.File, handoff *nativeEditorHandoff, runtime *nativeClientRuntime) error {
	c.handoff = handoff
	defer func() { c.handoff = nil }()
	path := handoff.path
	if handoff.editor != nil {
		file, err := os.CreateTemp("", "mekugi-draft-*.txt")
		if err != nil {
			c.notice = err.Error()
			return nil
		}
		path = file.Name()
		_, writeErr := file.WriteString(handoff.editor.Text)
		if err := errors.Join(writeErr, file.Close()); err != nil {
			_ = os.Remove(path)
			c.notice = err.Error()
			return nil
		}
	}
	done := make(chan error, 1)
	editorContext, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() { done <- runEditor(editorContext, path, stdin, stdout) }()
	var err error
	var connectionError error
wait:
	for {
		runtime.schedule()
		select {
		case message := <-runtime.messages:
			connectionError = runtime.deliver(message)
			if runtime.active.quit && connectionError == nil {
				connectionError = &sessionapi.NativeExit{}
			}
			if connectionError != nil {
				cancel()
				<-done
				err = connectionError
				break wait
			}
		case call := <-runtime.results:
			call.complete = true
			runtime.settle()
		case result := <-runtime.reattached:
			runtime.reattach(result, stdout)
		case err = <-done:
			break wait
		case <-ctx.Done():
			cancel()
			<-done
			err = ctx.Err()
			connectionError = err
			break wait
		}
		runtime.settle()
		runtime.retire()
	}
	c.renderer.terminal.Frame.Rows = nil
	if handoff.editor == nil {
		if err != nil {
			c.notice = err.Error()
		}
		return connectionError
	}
	if err == nil {
		editor := handoff.editor
		if !c.retainsEditor(editor) || editor.Revision() != handoff.target.Revision || c.event.Frame.View.Version.Attachment != handoff.attachment {
			err = errors.New("the original editor changed")
		} else {
			var data []byte
			data, err = os.ReadFile(path)
			if err == nil {
				err = editor.ApplyExternal(string(data))
			}
		}
	}
	if err != nil {
		c.notice = fmt.Sprintf("Editor: %v · draft preserved at %s", err, path)
		return connectionError
	}
	_ = os.Remove(path)
	c.notice = "Draft updated from editor"
	return connectionError
}

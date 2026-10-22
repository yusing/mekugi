package sessionapi

import (
	"context"
	"errors"
	"testing"

	activity "github.com/yusing/mekugi/modules/activity/presentation"
	"github.com/yusing/mekugi/modules/conversation/composer"
)

func TestNativeServerBacklogPreservesDeliveriesBeforeWriterAdmission(t *testing.T) {
	finish := make(chan struct{})
	launcher := &nativeStartupLauncher{run: func(ctx context.Context, start Start, server *Server) error {
		request := <-server.Native().Requests()
		frame := nativeTestFrame("", 1)
		frame.View.Info.Workspace = start.Workspace
		// Withhold the attachment acknowledgement so the real RPC writer cannot
		// consume any of these publications while the owner accumulates progress.
		for revision := uint64(1); revision <= 200; revision++ {
			frame.Switch = nil
			if revision == 80 {
				frame.View.Main.ID = "confirmed"
				frame.View.Version.Attachment = 2
				frame.Switch = &NativeSwitch{To: "confirmed", Origin: request.Attachment}
			}
			frame.MainOutput = []activity.OutputResult{{Demand: activity.OutputDemand{Source: revision}}}
			frame.AgentOutput = []activity.OutputResult{{Demand: activity.OutputDemand{Source: revision}}}
			if revision == 2 || revision == 121 {
				frame.Effects = append(frame.Effects, NativeEffect{ID: uint64(len(frame.Effects) + 1), Restore: &composer.Draft{Text: "retained input"}})
			}
			frame.View.Version.Revision = revision
			if err := server.Native().Publish(request.Attachment, frame); err != nil {
				return err
			}
		}
		request.Reply <- ""
		select {
		case <-finish:
			return &NativeExit{Code: 17, Message: "joined backlog outcome"}
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	hub := NewHub(t.Context(), launcher)
	defer hub.Close()
	client, _ := serveControl(t, hub.Handler())
	connection, err := StartNative(t.Context(), client, "http://mekugi", NativeStart{Workspace: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	close(finish)
	var previous uint64
	var switches, mainOutput, agentOutput int
	var last NativeEvent
	for {
		event, err := connection.Next()
		if err != nil {
			outcome, ok := errors.AsType[*NativeExit](err)
			if !ok || outcome.Code != 17 || outcome.Message != "joined backlog outcome" {
				t.Fatalf("backlog lost joined exit: %v", err)
			}
			break
		}
		if event.Frame.View.Version.Revision <= previous {
			t.Fatalf("backlog reordered revisions: %d after %d", event.Frame.View.Version.Revision, previous)
		}
		previous = event.Frame.View.Version.Revision
		if event.Frame.Switch != nil {
			switches++
		}
		for _, output := range event.Frame.MainOutput {
			mainOutput++
			if output.Demand.Source != uint64(mainOutput) {
				t.Fatalf("main output lost order: %d at %d", output.Demand.Source, mainOutput)
			}
		}
		for _, output := range event.Frame.AgentOutput {
			agentOutput++
			if output.Demand.Source != uint64(agentOutput) {
				t.Fatalf("agent output lost order: %d at %d", output.Demand.Source, agentOutput)
			}
		}
		last = event
	}
	if previous != 200 || last.Frame.View.Main.ID != "confirmed" || len(last.Frame.Effects) != 2 || switches != 1 || mainOutput != 200 || agentOutput != 200 {
		t.Fatalf("lost backlog delivery: revision=%d thread=%q effects=%d switches=%d mainOutput=%d agentOutput=%d", previous, last.Frame.View.Main.ID, len(last.Frame.Effects), switches, mainOutput, agentOutput)
	}
}

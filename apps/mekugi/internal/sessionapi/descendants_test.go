package sessionapi

import (
	"context"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
)

func TestHubDescendantPublicationAndReadOnlyObservation(t *testing.T) {
	hub, _, _ := preparedParent(t)
	workspace := t.TempDir()
	requests := make(chan NativeRequest, 4)
	frames := make(chan NativeFrame, 4)
	childAPI := make(chan *Server, 1)
	_, err := hub.StartPrepared(t.Context(), "parent", "", workspace, func(ctx context.Context, server *Server) error {
		childAPI <- server
		if err := server.Publish(Frame{Session: Session{Thread: "child", Workspace: workspace}, Caption: sessionpresentation.Caption{Model: "child-model", Effort: "low"}, Main: &activity.Publication{LastSeq: 7}}); err != nil {
			return err
		}
		if err := server.SetSummary(Summary{Thread: "child", Workspace: workspace, Ready: true}); err != nil {
			return err
		}
		var view string
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case request := <-server.Native().Requests():
				view = request.Attachment
				requests <- request
				request.Reply <- ""
			case frame := <-frames:
				if err := server.Native().Publish(view, frame); err != nil {
					return err
				}
			}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	server := <-childAPI
	children := hub.Descendants("parent")
	if len(children) != 1 || children["child"].Session.Workspace != workspace || children["child"].Caption.Model != "child-model" || children["child"].Main.LastSeq != 7 {
		t.Fatalf("child publication: %+v", children)
	}
	if _, err := hub.Observe(t.Context(), "child", "parent"); err == nil {
		t.Fatal("child observed ancestor runtime")
	}
	observer, err := hub.Observe(t.Context(), "parent", "child")
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Close()
	request := <-requests
	if !request.Attach || request.Thread != "child" || request.Workspace != workspace {
		t.Fatalf("observation attachment: %+v", request)
	}
	// Reads admitted before the initial snapshot wait for its usable version.
	if err := observer.Read(NativeOperation{Details: &NativeDetailRequest{Kind: NativeEntryDetails, Source: 7}}); err != nil {
		t.Fatal(err)
	}
	frame := nativeTestFrame("child", 3)
	frame.View.Info.Workspace = workspace
	frame.View.Caption = children["child"].Caption
	frame.Main = children["child"].Main
	frames <- frame
	request = <-requests
	if request.Operation.Details == nil || request.Version != frame.View.Version {
		t.Fatalf("early detail read lost its admitted version: %+v", request)
	}
	deadline := time.After(3 * time.Second)
	for {
		event, err := observer.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		if event.Frame.Main != nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("missing child snapshot")
		case <-time.After(time.Millisecond):
		}
	}
	if err := observer.Read(NativeOperation{Focused: new(true)}); err == nil {
		t.Fatal("observation admitted non-read operation")
	}
	demand := activity.OutputDemand{Source: 7, Output: 1}
	if err := observer.Read(NativeOperation{Output: &NativeOutputDemand{Demands: []activity.OutputDemand{demand}}}); err != nil {
		t.Fatal(err)
	}
	request = <-requests
	if request.Attach || request.Operation.Output.Demands[0] != demand || request.Version != frame.View.Version {
		t.Fatalf("read lost source/version: %+v", request)
	}
	observer.Close()
	if _, err := hub.Requests("child"); err == nil {
		t.Fatal("test child unexpectedly has request owner")
	}
	if !hub.activeThread("child") {
		t.Fatal("closing observation stopped child")
	}
	if err := server.Publish(Frame{Session: Session{Thread: "child", Workspace: workspace}, Main: &activity.Publication{LastSeq: 8}}); err != nil {
		t.Fatal(err)
	}
	if children["child"].Main.LastSeq != 7 || hub.Descendants("parent")["child"].Caption.Model != "" {
		t.Fatal("publication mutation or missing settings acquired defaults")
	}
}

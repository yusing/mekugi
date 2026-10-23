package sessionapi

import (
	"bytes"
	json "encoding/json/v2"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
	changes "github.com/yusing/mekugi/modules/changehistory/presentation"
	"github.com/yusing/mekugi/modules/conversation/composer"
	conversation "github.com/yusing/mekugi/modules/conversation/presentation"
	"github.com/yusing/mekugi/modules/livepreview/preview"
)

func TestRemoteRecoveryProjectionBoundsAndDetachment(t *testing.T) {
	t.Parallel()
	draft := composer.Draft{Text: strings.Repeat("a", 511) + "界tail", Images: []composer.Token{{Path: "/private/image.png"}}, Attachments: []string{"private-snapshot"}, AnswerImages: []string{"/private/answer.png"}}
	frame := nativeTestFrame("thread", 1)
	frame.Effects = []NativeEffect{{ID: 3, Thread: "thread", Restore: &draft}, {ID: 4, Thread: "thread", ShellRestore: &composer.Draft{Text: "pwd"}}}
	remote := frame.remote()
	for i, effect := range remote.Effects {
		if effect.ID != frame.Effects[i].ID || effect.Thread != "thread" || effect.Restore != nil || effect.ShellRestore != nil || effect.Recovery == nil {
			t.Fatalf("recovery identity or snapshot projection: %+v", effect)
		}
	}
	preview := remote.Effects[0].Recovery
	if !utf8.ValidString(preview.Preview) || preview.Preview != strings.Repeat("a", 511)+"…" || !preview.Attachments || preview.Shell {
		t.Fatalf("unbounded or inaccurate preview: %+v", preview)
	}
	if got := remote.Effects[1].Recovery; got.Preview != "pwd" || !got.Shell || got.Attachments {
		t.Fatalf("shell preview: %+v", got)
	}
	data, err := json.Marshal(&remote, nativeEncoding)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"/private/", "private-snapshot", "界tail"} {
		if bytes.Contains(data, []byte(secret)) {
			t.Fatalf("remote frame exposes retained snapshot %q", secret)
		}
	}
	remote.Effects[0].Thread = "changed"
	remote.Effects[0].Recovery.Preview = "changed"
	if frame.Effects[0].Recovery != nil || frame.Effects[0].Thread != "thread" || !reflect.DeepEqual(*frame.Effects[0].Restore, draft) {
		t.Fatal("public projection mutated native restoration")
	}
}

func TestNativeRecoveryDraftExcludesTargetedEditorEffects(t *testing.T) {
	t.Parallel()
	draft := &composer.Draft{Text: "retained"}
	for _, effect := range []NativeEffect{{Target: &conversation.EditorTarget{}, Restore: draft}, {Action: &composer.Action{}, Restore: draft}, {}} {
		if effect.RecoveryDraft() != nil {
			t.Fatal("editor-local effect became a retained submission")
		}
	}
	if (NativeEffect{Restore: draft}).RecoveryDraft() != draft || (NativeEffect{ShellRestore: draft}).RecoveryDraft() != draft {
		t.Fatal("unbound restoration lost its authoritative draft")
	}
}

func TestRemoteRecoveryTransportAdmission(t *testing.T) {
	t.Parallel()
	s := NewLive()
	hub := NewHub(t.Context(), nil)
	registerTestSession(hub, "thread", s, Summary{Thread: "thread", Workspace: "/workspace"})
	handler := hub.Handler()
	event, _, _ := openRemoteTest(t, s, handler, nativeTestFrame("thread", 1))
	for _, operation := range []NativeOperation{
		{Recovery: &NativeRecoveryInput{Effect: 1, Action: "unknown"}},
		{Recovery: &NativeRecoveryInput{Effect: 1, Action: EditRecovery, Text: new([]string{"invalid"})}},
		{Recovery: &NativeRecoveryInput{Effect: 1, Action: RetryRecovery}, Command: new("/status")},
	} {
		if got := (<-remotePost(t, handler, false, event, operation)).Code; got != 400 {
			t.Fatalf("invalid recovery reached owner: %d", got)
		}
	}
	operation := NativeOperation{Recovery: &NativeRecoveryInput{Effect: 1, Action: RetryRecovery}}
	for _, invalid := range []NativeEvent{
		{Attachment: "foreign", Frame: event.Frame},
		{Attachment: event.Attachment, Frame: nativeTestFrame("thread", 2)},
	} {
		if got := (<-remotePost(t, handler, false, invalid, operation)).Code; got != 409 {
			t.Fatalf("foreign identity reached owner: %d", got)
		}
	}
	if got := (<-remotePost(t, handler, true, event, operation)).Code; got != 409 {
		t.Fatalf("native route accepted remote recovery attachment: %d", got)
	}
	next := nativeTestFrame("thread", 1)
	next.View.Version.Revision++
	if err := s.Native().Publish(event.Attachment, next); err != nil {
		t.Fatal(err)
	}
	for _, action := range []RecoveryAction{RetryRecovery, EditRecovery, DiscardRecovery} {
		operation.Recovery.Action = action
		done := remotePost(t, handler, false, event, operation)
		request := <-s.Native().Requests()
		if !request.RemoteHuman() || request.Operation.Recovery == nil || *request.Operation.Recovery != *operation.Recovery || request.Prepared != nil {
			t.Fatal("transport changed retained recovery or reacquired attachments")
		}
		request.Reply <- ""
		if got := (<-done).Code; got != 202 {
			t.Fatalf("identity-addressed recovery rejected older publication: %d", got)
		}
	}
}

func TestRemoteFrameOmitsTerminalRendererState(t *testing.T) {
	t.Parallel()
	frame := nativeTestFrame("thread", 1)
	frame.View.Displayed = frame.View.Main
	frame.Main, frame.Agents = &activity.Publication{}, &activity.Publication{}
	frame.Review = &changes.ReviewView{Workspace: "/workspace"}
	frame.Live, frame.FileLinks = []preview.Preview{{}}, map[string]bool{"/workspace/a": true}
	frame.MainOutput, frame.AgentOutput, frame.ReacquireOutput = []activity.OutputResult{{}}, []activity.OutputResult{{}}, true
	frame.Activity = &NativeActivityFrame{Reset: 1}
	remote := frame.remote()
	if remote.Main != nil || remote.Agents != nil || remote.Review != nil || remote.Live != nil || remote.FileLinks != nil ||
		remote.MainOutput != nil || remote.AgentOutput != nil || remote.ReacquireOutput || remote.Activity != nil {
		t.Fatalf("remote frame repeats terminal renderer state: %+v", remote)
	}
	if frame.Main == nil || frame.Review == nil || frame.Activity == nil {
		t.Fatal("remote projection mutated the native frame")
	}
	frame.View.Displayed = sessionpresentation.Thread{ID: "child", Caller: "/root/worker"}
	if remote := frame.remote(); remote.Main != nil || remote.Agents != nil {
		t.Fatal("displayed child repeated the parent's terminal feeds")
	}
	frame.View.SideThread = sessionpresentation.Thread{ID: "side", Caller: "/root"}
	frame.View.Displayed = frame.View.SideThread
	if frame.remote().Main == nil {
		t.Fatal("a displayed side conversation lost its only publication")
	}
}

func TestRemoteRecoveryEditingExposesTextAndLabelsOnly(t *testing.T) {
	draft := composer.Draft{Text: "edit @file", Files: []composer.Token{{Start: 5, End: 10, Path: "/private/file"}}, Attachments: []string{"private snapshot"}}
	frame := nativeTestFrame("thread", 1)
	frame.Effects = []NativeEffect{{ID: 1, Restore: &draft, Recovery: &NativeRecoveryView{Editing: true}}}
	remote := frame.remote()
	if !reflect.DeepEqual(remote.Effects[0].Recovery.Parts, []string{"edit ", "@file", ""}) {
		t.Fatal(remote.Effects[0].Recovery)
	}
	data, err := json.Marshal(remote, nativeEncoding)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("private")) {
		t.Fatal("editor exposed retained attachment contents or paths")
	}
	if frame.Effects[0].Recovery.Parts != nil {
		t.Fatal("projection mutated retained state")
	}
}

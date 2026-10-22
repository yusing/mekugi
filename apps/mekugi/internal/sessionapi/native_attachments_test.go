package sessionapi

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"
	"github.com/yusing/mekugi/modules/conversation/composer"
)

func TestNativeAcquisitionLeavesOwnerResponsiveAndCleansRefusal(t *testing.T) {
	t.Parallel()
	native, client := nativeTestServer(t)
	started, release := make(chan struct{}), make(chan struct{})
	image := filepath.Join(t.TempDir(), "request-owned-image")
	if err := os.WriteFile(image, []byte("prepared image"), 0600); err != nil {
		t.Fatal(err)
	}
	native.prepare = func(ctx context.Context, workspace string, input InputRequest) (*PreparedInput, error) {
		if workspace != "/workspace" || input.NativeDraft == nil || input.NativeDraft.Text != "draft" {
			t.Error("native acquisition lost its captured workspace/draft")
		}
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return NewPreparedInput(composer.Draft{Text: "prepared"}, []string{image}), nil
	}
	frame := nativeTestFrame("thread", 1)
	connection, attachment := openNativeTest(t, native, client, frame, "")
	event, err := connection.Next()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := connection.Send(t.Context(), event.Target(), NativeOperation{Composer: &NativeComposerInput{Intent: sessionpresentation.ComposerIntent{Operation: sessionpresentation.SubmitComposer}, Draft: composer.Draft{Text: "draft"}}})
		done <- err
	}()
	<-started
	command := make(chan error, 1)
	go func() {
		_, err := connection.Send(t.Context(), event.Target(), NativeOperation{Command: new("/status")})
		command <- err
	}()
	request := <-native.Requests()
	if request.Operation.Command == nil || request.Prepared != nil {
		t.Fatal("acquisition entered the execution loop before preparation completed")
	}
	request.Reply <- ""
	if err := <-command; err != nil {
		t.Fatalf("independent native operation blocked during acquisition: %v", err)
	}
	frame.View.Version.Revision++
	if err := native.Publish(attachment.Attachment, frame); err != nil {
		t.Fatal(err)
	}
	close(release)
	select {
	case request = <-native.Requests():
	case err := <-done:
		t.Fatalf("publication progress prevented prepared composer admission: %v", err)
	}
	if request.Version != event.Frame.View.Version {
		t.Fatal("acquisition silently replaced the captured composer version")
	}
	if request.Prepared == nil || request.Prepared.Draft.Text != "prepared" || request.Thread != "thread" || request.Workspace != "/workspace" {
		t.Fatal("native admission lost its snapshot or captured target")
	}
	request.Reply <- "The session changed during preparation."
	if err := <-done; err == nil {
		t.Fatal("native admission refusal was acknowledged")
	}
	if _, err := os.Stat(image); !os.IsNotExist(err) {
		t.Fatalf("refused native acquisition retained request-owned image: %v", err)
	}
}

// Preparation capacity bounds acquisition, independently of semantic requests
// and of the execution owner's later admission/reply lifetime.
func TestNativePreparationCapacityLeavesSemanticAdmissionResponsive(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	started := make(chan string, 5)
	release := make(chan struct{})
	prepare := func(ctx context.Context, _ string, input InputRequest) (*PreparedInput, error) {
		text := input.NativeDraft.Text
		started <- text
		if text == "blocked" {
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return NewPreparedInput(composer.Draft{Text: text}, nil), nil
	}
	first, second := NewPreparedLive(prepare), NewPreparedLive(prepare)
	hub := NewHub(ctx, nil)
	registerTestSession(hub, "first", first, Summary{Thread: "first", Workspace: "/workspace"})
	registerTestSession(hub, "second", second, Summary{Thread: "second", Workspace: "/workspace"})
	client, _ := serveControl(t, hub.Handler())
	left, _ := openNativeTest(t, first.Native(), client, nativeTestFrame("first", 1), "")
	right, _ := openNativeTest(t, second.Native(), client, nativeTestFrame("second", 1), "")
	leftEvent, err := left.Next()
	if err != nil {
		t.Fatal(err)
	}
	rightEvent, err := right.Next()
	if err != nil {
		t.Fatal(err)
	}
	composerOperation := func(text string) NativeOperation {
		return NativeOperation{Composer: &NativeComposerInput{Intent: sessionpresentation.ComposerIntent{Operation: sessionpresentation.SubmitComposer}, Draft: composer.Draft{Text: text}}}
	}
	pending := make(chan error, 4)
	for range 4 {
		go func() {
			_, err := left.Send(ctx, leftEvent.Target(), composerOperation("blocked"))
			pending <- err
		}()
	}
	for range 4 {
		select {
		case <-started:
		case err := <-pending:
			t.Fatalf("acquisition refused before filling capacity: %v", err)
		case <-ctx.Done():
			t.Fatal("acquisitions did not start")
		}
	}
	// Capacity is shared across the hub's borrowed servers, not just one owner.
	if _, err := right.Send(ctx, rightEvent.Target(), composerOperation("probe")); err == nil {
		t.Fatal("fifth concurrent acquisition bypassed aggregate capacity")
	} else if refusal, ok := errors.AsType[*NativeFailure](err); !ok || refusal.Status != http.StatusServiceUnavailable {
		t.Fatalf("aggregate capacity returned a different failure: %v", err)
	}
	select {
	case <-started:
		t.Fatal("over-capacity request reached acquisition")
	default:
	}
	for _, operation := range []NativeOperation{
		{Interrupt: &sessionpresentation.InterruptIntent{Version: rightEvent.Frame.View.Version}},
		{Receipt: &NativeReceipt{Effect: 1}},
	} {
		done := make(chan error, 1)
		go func() { _, err := right.Send(ctx, rightEvent.Target(), operation); done <- err }()
		select {
		case request := <-second.Native().Requests():
			if request.Prepared != nil || (request.Operation.Interrupt == nil) != (operation.Interrupt == nil) || (request.Operation.Receipt == nil) != (operation.Receipt == nil) {
				t.Fatal("semantic operation changed or entered acquisition")
			}
			request.Reply <- ""
		case err := <-done:
			t.Fatalf("full acquisition capacity rejected a semantic operation: %v", err)
		case <-ctx.Done():
			t.Fatal("semantic operation blocked behind acquisition")
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	close(release)
	var waiting []NativeRequest
	select {
	case request := <-first.Native().Requests():
		waiting = append(waiting, request)
	case err := <-pending:
		t.Fatalf("prepared request did not reach owner: %v", err)
	case <-ctx.Done():
		t.Fatal("prepared requests did not reach owner")
	}
	// One request awaits its owner reply, while three remain queued for owner
	// admission. All four acquisition slots must already be reusable.
	probe := make(chan error, 4)
	for range 4 {
		go func() {
			_, err := right.Send(ctx, rightEvent.Target(), composerOperation("probe"))
			probe <- err
		}()
	}
	var probes []NativeRequest
	for range 4 {
		select {
		case request := <-second.Native().Requests():
			if request.Prepared == nil || request.Prepared.Draft.Text != "probe" {
				t.Fatal("probe lost its prepared snapshot")
			}
			probes = append(probes, request)
		case err := <-probe:
			t.Fatalf("owner admission/reply wait retained acquisition capacity: %v", err)
		case <-ctx.Done():
			t.Fatal("owner admission/reply wait blocked new acquisition")
		}
	}
	for _, request := range probes {
		request.Reply <- ""
	}
	for range 4 {
		if err := <-probe; err != nil {
			t.Fatal(err)
		}
	}
	for range 3 {
		select {
		case request := <-first.Native().Requests():
			waiting = append(waiting, request)
		case err := <-pending:
			t.Fatalf("prepared request did not reach owner: %v", err)
		case <-ctx.Done():
			t.Fatal("prepared requests did not reach owner")
		}
	}
	for _, request := range waiting {
		request.Reply <- ""
	}
	for range 4 {
		if err := <-pending; err != nil {
			t.Fatal(err)
		}
	}
}

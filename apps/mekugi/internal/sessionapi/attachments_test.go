package sessionapi

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/modules/conversation/composer"
)

func TestAttachmentPreparationDoesNotBlockOwner(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	workspace := t.TempDir()
	path := filepath.Join(workspace, "acquired.png")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	s := NewPreparedLive(func(ctx context.Context, cwd string, input InputRequest) (*PreparedInput, error) {
		if cwd != workspace || len(input.Attachments) != 1 {
			t.Errorf("preparation lost captured request: cwd=%s input=%+v", cwd, input)
		}
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return NewPreparedInput(composer.Draft{Text: "snapshot"}, []string{path}), nil
	})
	if err := s.SetSummary(Summary{Thread: "original", Workspace: workspace}); err != nil {
		t.Fatal(err)
	}
	body := `{"attachments":[{"source":"upload","name":"image","data":""}]}`
	var response testReply
	done := make(chan struct{})
	go func() {
		result, failure := s.request(ctx, requestTarget{"original", workspace}, "session/input", []byte(body), make(chan struct{}, preparationLimit))
		response = ownerReply(t, result, failure)
		close(done)
	}()
	<-started
	stopDone := make(chan struct{})
	go func() {
		s.request(ctx, requestTarget{"original", workspace}, "session/interrupt", nil, make(chan struct{}, preparationLimit))
		close(stopDone)
	}()
	select {
	case intent := <-s.Intents():
		if intent.Action != Stop {
			t.Fatal("acquisition occupied owner admission")
		}
		intent.Reply <- ""
	case <-ctx.Done():
		t.Fatal("interrupt blocked behind acquisition")
	}
	<-stopDone
	if err := s.SetSummary(Summary{Thread: "replacement", Workspace: workspace + "/changed"}); err != nil {
		t.Fatal(err)
	}
	close(release)
	select {
	case intent := <-s.Intents():
		if intent.Thread != "original" || intent.Workspace != workspace || intent.Prepared.Draft.Text != "snapshot" {
			t.Fatalf("preparation retargeted on completion: %+v", intent)
		}
		intent.Reply <- "The session target changed."
	case <-ctx.Done():
		t.Fatal("prepared input not delivered")
	}
	<-done
	if response.Code != http.StatusConflict {
		t.Fatalf("refusal: %d %s", response.Code, response.Body)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("refused acquired image not cleaned", err)
	}
}

func TestAttachmentCanceledAfterDeliveryCannotTransfer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "acquired")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	prepared := NewPreparedInput(composer.Draft{Text: "snapshot"}, []string{path})
	s := NewPreparedLive(func(context.Context, string, InputRequest) (*PreparedInput, error) { return prepared, nil })
	ctx, cancel := context.WithCancel(t.Context())
	var response testReply
	done := make(chan struct{})
	go func() {
		result, failure := s.request(ctx, requestTarget{}, "session/input", []byte(`{"text":"input","attachments":[{"source":"path","path":"image"}]}`), make(chan struct{}, preparationLimit))
		response = ownerReply(t, result, failure)
		close(done)
	}()
	intent := <-s.Intents()
	cancel()
	<-done
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "unknown") {
		t.Fatalf("ambiguous cancellation became success: %d %s", response.Code, response.Body)
	}
	if prepared.Admit(intent.Context, make(map[string]bool)) {
		t.Fatal("late completion transferred canceled files")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("canceled acquired image not cleaned", err)
	}
}

func TestAttachmentTransportBudgetAndNotice(t *testing.T) {
	// A maximum-size upload plus ordinary text fits the JSON transport. The
	// execution owner separately validates decoded content and envelope limits.
	encodedSize := (UploadLimit + 2) / 3 * 4
	s := NewPreparedLive(func(_ context.Context, _ string, input InputRequest) (*PreparedInput, error) {
		if len(input.Attachments) != 1 || len(*input.Attachments[0].Data) != encodedSize || input.Text != "inspect" {
			t.Error("transport changed upload descriptor or text")
		}
		return NewPreparedInput(composer.Draft{Text: "inspect", AttachmentNotice: "File contents omitted: upload."}, nil), nil
	})
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	hub := NewHub(ctx, nil)
	registerTestSession(hub, "thread", s, Summary{Thread: "thread", Workspace: "/workspace"})
	client := openTestRPC(t, hub.Handler(), false)
	body := `{"text":"inspect","attachments":[{"source":"upload","name":"upload","data":"` + strings.Repeat("A", encodedSize-1) + `="}]}`
	var response jsontext.Value
	var requestErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		requestErr = client.Call(ctx, "session/input", RPCParams{Thread: "thread", Body: []byte(body)}, &response)
	}()
	defer func() { cancel(); <-done }()
	select {
	case intent := <-s.Intents():
		if !intent.Prepared.Admit(intent.Context, make(map[string]bool)) {
			t.Error("prepared upload refused")
		}
		intent.Reply <- ""
	case <-done:
		t.Fatalf("maximum upload ended before admission: %v", requestErr)
	case <-ctx.Done():
		t.Fatal("maximum upload did not reach admission")
	}
	<-done
	if requestErr != nil || !strings.Contains(string(response), `"notice":"File contents omitted: upload."`) {
		t.Fatalf("upload acknowledgement: %s, %v", response, requestErr)
	}
}

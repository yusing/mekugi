package sessionapi

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
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

type attachmentLauncher struct {
	prepare func(context.Context, string, InputRequest) (*PreparedInput, error)
	run     func(context.Context, Start, *Server) error
}

func (l attachmentLauncher) StartOptions(context.Context) (StartOptions, error) {
	return StartOptions{Models: []ModelGroup{{Models: []Model{{ID: "model"}}}}}, nil
}
func (l attachmentLauncher) PrepareInput(ctx context.Context, cwd string, input InputRequest) (*PreparedInput, error) {
	return l.prepare(ctx, cwd, input)
}
func (l attachmentLauncher) Run(ctx context.Context, start Start, server *Server) error {
	return l.run(ctx, start, server)
}

func TestManagedAttachmentInitialRetryKeepsSnapshot(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var prepared atomic.Int32
	var snapshot *PreparedInput
	updates := make(chan []byte, 8)
	launcher := attachmentLauncher{
		prepare: func(context.Context, string, InputRequest) (*PreparedInput, error) {
			prepared.Add(1)
			snapshot = NewPreparedInput(composer.Draft{Text: "captured", AttachmentNotice: "file contents omitted"}, nil)
			return snapshot, nil
		},
		run: func(ctx context.Context, start Start, s *Server) error {
			if err := s.SetSummary(Summary{Thread: "thread", Workspace: start.Workspace, Ready: true}); err != nil {
				return err
			}
			select {
			case first := <-s.Intents():
				if err := s.SetSummary(Summary{Thread: "thread", Workspace: start.Workspace, Ready: true, Waiting: 1}); err != nil {
					return err
				}
				// Wait for the hub's list publication, proving that the newer
				// summary was consumed while the first answer was outstanding.
				observed := false
				for !observed {
					select {
					case update := <-updates:
						observed = bytes.Contains(update, []byte(`"waiting":1`))
					case <-ctx.Done():
						return ctx.Err()
					}
				}
				first.Reply <- "Try again when ready."
			case <-ctx.Done():
				return ctx.Err()
			}
			select {
			case intent := <-s.Intents():
				if intent.Prepared != snapshot || intent.Prepared.Draft.Text != "captured" {
					t.Error("first input was reacquired on retry")
				}
				if !intent.Prepared.Admit(intent.Context, make(map[string]bool)) {
					t.Error("retry lost snapshot ownership")
				}
				intent.Reply <- ""
			case <-ctx.Done():
				return ctx.Err()
			}
			<-ctx.Done()
			return ctx.Err()
		},
	}
	hub := NewHub(ctx, launcher)
	hub.viewers[updates] = struct{}{}
	defer hub.Close()
	response := managedRequest(t, hub, "session/start", "", Start{Workspace: t.TempDir(), Model: "model", Attachments: []Attachment{{Source: "upload", Name: "empty.txt", Data: new("")}}})
	if response.Code != http.StatusAccepted || prepared.Load() != 1 || !snapshot.Admitted() {
		t.Fatalf("first attachment: status=%d preparations=%d admitted=%v", response.Code, prepared.Load(), snapshot.Admitted())
	}
}

func TestManagedAttachmentCancellationBeforeInitialAdmission(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	path := filepath.Join(t.TempDir(), "acquired")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	started, stopped := make(chan struct{}), make(chan struct{})
	launcher := attachmentLauncher{
		prepare: func(context.Context, string, InputRequest) (*PreparedInput, error) {
			return NewPreparedInput(composer.Draft{Text: "snapshot"}, []string{path}), nil
		},
		run: func(ctx context.Context, _ Start, s *Server) error {
			close(started)
			defer close(stopped)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-s.Intents():
				t.Error("unready canceled first input was submitted")
				return nil
			}
		},
	}
	hub := NewHub(ctx, launcher)
	defer hub.Close()
	body, _ := json.Marshal(Start{Workspace: t.TempDir(), Model: "model", Attachments: []Attachment{{Source: "upload", Name: "image", Data: new("")}}})
	requestCtx, disconnect := context.WithCancel(ctx)
	finished := make(chan struct{})
	go func() {
		hub.startSession(requestCtx, body)
		close(finished)
	}()
	<-started
	disconnect()
	<-finished
	select {
	case <-stopped:
	case <-ctx.Done():
		t.Fatal("canceled first-input runtime not joined")
	}
	hub.Close()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("canceled first-input image survived", err)
	}
}

func TestManagedAttachmentDisconnectAfterAdmission(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	path := filepath.Join(t.TempDir(), "acquired")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	admitted, reply, stopped := make(chan struct{}), make(chan struct{}), make(chan struct{})
	launcher := attachmentLauncher{
		prepare: func(context.Context, string, InputRequest) (*PreparedInput, error) {
			return NewPreparedInput(composer.Draft{Text: "snapshot"}, []string{path}), nil
		},
		run: func(ctx context.Context, start Start, s *Server) error {
			defer close(stopped)
			if err := s.SetSummary(Summary{Thread: "thread", Workspace: start.Workspace, Ready: true}); err != nil {
				return err
			}
			select {
			case intent := <-s.Intents():
				if !intent.Prepared.Admit(intent.Context, make(map[string]bool)) {
					t.Error("first input refused")
				}
				close(admitted)
				<-reply
				intent.Reply <- ""
			case <-ctx.Done():
				return ctx.Err()
			}
			<-ctx.Done()
			return ctx.Err()
		},
	}
	hub := NewHub(ctx, launcher)
	defer hub.Close()
	body, _ := json.Marshal(Start{Workspace: t.TempDir(), Model: "model", Attachments: []Attachment{{Source: "upload", Name: "image", Data: new("")}}})
	requestCtx, disconnect := context.WithCancel(ctx)
	finished := make(chan struct{})
	go func() {
		hub.startSession(requestCtx, body)
		close(finished)
	}()
	<-admitted
	disconnect()
	<-finished
	close(reply)
	if _, err := os.Stat(path); err != nil {
		t.Fatal("disconnect reclaimed admitted image", err)
	}
	select {
	case <-stopped:
		t.Fatal("disconnect stopped admitted execution")
	default:
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

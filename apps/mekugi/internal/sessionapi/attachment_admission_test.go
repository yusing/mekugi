package sessionapi

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	"github.com/yusing/mekugi/modules/conversation/composer"
)

func TestManagedInitialInputRejectsReplacement(t *testing.T) {
	for _, test := range []struct {
		name        string
		attachments bool
		refuseFirst bool
	}{
		{name: "attachments after refusal", attachments: true, refuseFirst: true},
		{name: "plain text after refusal", refuseFirst: true},
		{name: "replacement before ready", attachments: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			targets := make(chan string, 2)
			updates := make(chan []byte, 8)
			launcher := attachmentLauncher{
				prepare: func(context.Context, string, InputRequest) (*PreparedInput, error) {
					return NewPreparedInput(composer.Draft{Text: "snapshot"}, nil), nil
				},
				run: func(ctx context.Context, start Start, s *Server) error {
					if err := s.SetSummary(Summary{Thread: "original", Workspace: start.Workspace, Ready: test.refuseFirst}); err != nil {
						return err
					}
					// Confirm the first logical identity was observed, even when
					// it is not ready to receive input yet.
					for observed := false; !observed; {
						select {
						case update := <-updates:
							observed = bytes.Contains(update, []byte(`"thread":"original"`))
						case <-ctx.Done():
							return ctx.Err()
						}
					}
					if test.refuseFirst {
						select {
						case intent := <-s.Intents():
							targets <- intent.Thread
							intent.Reply <- "Answer the open prompt first."
						case <-ctx.Done():
							return ctx.Err()
						}
					}
					if err := s.SetSummary(Summary{Thread: "replacement", Workspace: start.Workspace, Ready: true}); err != nil {
						return err
					}
					select {
					case intent := <-s.Intents():
						targets <- intent.Thread
						intent.Prepared.Admit(intent.Context, make(map[string]bool))
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
			start := Start{Workspace: t.TempDir(), Model: "model", Text: "first"}
			if test.attachments {
				start.Attachments = []Attachment{{Source: "upload", Name: "notes", Data: new("")}}
			}
			response := managedRequest(t, hub, "session/start", "", start)
			if response.Code == http.StatusAccepted {
				t.Error("replacement admitted the pending initial input")
			}
			for len(targets) > 0 {
				if target := <-targets; target != "original" {
					t.Errorf("initial input retargeted to %q", target)
				}
			}
		})
	}
}

func TestManagedAttachmentCancellationWhileOwnerUnavailable(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(t.TempDir(), "acquired")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		stopped := make(chan struct{})
		launcher := attachmentLauncher{
			prepare: func(context.Context, string, InputRequest) (*PreparedInput, error) {
				return NewPreparedInput(composer.Draft{Text: "snapshot"}, []string{path}), nil
			},
			run: func(ctx context.Context, start Start, s *Server) error {
				defer close(stopped)
				if err := s.SetSummary(Summary{Thread: "thread", Workspace: start.Workspace, Ready: true}); err != nil {
					return err
				}
				<-ctx.Done() // The owner never receives the initial intent.
				return ctx.Err()
			},
		}
		hub := NewHub(ctx, launcher)
		defer hub.Close()
		body, _ := json.Marshal(Start{Workspace: workspace, Model: "model", Attachments: []Attachment{{Source: "upload", Name: "image", Data: new("")}}})
		requestCtx, disconnect := context.WithCancel(ctx)
		var response testReply
		go func() {
			result, failure := hub.startSession(requestCtx, body)
			response = ownerReply(t, result, failure)
		}()
		synctest.Wait()
		if hub.sessions["thread"] == nil {
			t.Fatal("initial target was not ready")
		}
		disconnect()
		synctest.Wait()
		select {
		case <-stopped:
		default:
			t.Error("cancellation did not join the unadmitted runtime while sending")
		}
		if response.Code != http.StatusServiceUnavailable {
			t.Errorf("canceled admission returned %d", response.Code)
		}
		hub.Close()
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("canceled startup retained its acquired image", err)
		}
	})
}

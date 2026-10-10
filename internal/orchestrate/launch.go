package orchestrate

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

// Launch retains the host request and acknowledgements. Params is the
// serialized thread/start configuration, supplied by the runtime coordinator.
type Launch struct {
	Message      string         `json:"message"`
	Params       jsontext.Value `json:"params"`
	ThreadID     string         `json:"thread_id,omitempty"`
	ThreadResult jsontext.Value `json:"thread_result,omitempty"`
	TurnID       string         `json:"turn_id,omitempty"`
	HostStatus   string         `json:"host_status,omitempty"`
}

var errLaunchUnchanged = errors.New("launch unchanged")

// ObserveLaunch retains host outcomes independently of launch acknowledgements,
// which may arrive after a turn's lifecycle notifications.
func (s *Store) ObserveLaunch(ctx context.Context, workspace, main, name, status, message string) error {
	return s.withBatch(ctx, workspace, main, name, func(b *Batch) error {
		if b.Launch == nil {
			return errors.New("batch has no launch intent")
		}
		b.Launch.HostStatus, b.Error = status, message
		return nil
	})
}

// BeginLaunch reserves dispatch once. Only a true dispatch result authorizes
// thread/start; repeats return retained facts, including uncertain outcomes.
func (s *Store) BeginLaunch(ctx context.Context, workspace, main, name, message string, params jsontext.Value) (batch Batch, dispatch bool, err error) {
	params, err = launchObject(params)
	if message == "" || err != nil {
		return batch, false, errors.New("launch requires a handoff and host configuration object")
	}
	err = s.withBatch(ctx, workspace, main, name, func(b *Batch) error {
		batch = *b
		if b.Launch != nil {
			if b.Launch.Message != message || !bytes.Equal(b.Launch.Params, params) {
				return errors.New("batch already has a different launch request")
			}
			return errLaunchUnchanged
		}
		if b.State != "prepared" {
			return fmt.Errorf("batch is %s; launch requires a prepared checkout", b.State)
		}
		if err := validateLaunchCheckout(ctx, *b); err != nil {
			return err
		}
		b.State = "starting"
		b.Launch = &Launch{Message: message, Params: params}
		batch, dispatch = *b, true
		return nil
	})
	// A failed publication must never authorize the host effect.
	return batch, dispatch && err == nil, err
}

func validateLaunchCheckout(ctx context.Context, b Batch) error {
	if err := validateCheckoutIdentity(ctx, b); err != nil {
		return err
	}
	base, err := sourceBase(ctx, b.VCS, b.Cwd)
	if err != nil || base != b.Base {
		return errors.New("prepared checkout baseline changed")
	}
	return nil
}

func validateCheckoutIdentity(ctx context.Context, b Batch) error {
	checkout, err := filepath.EvalSymlinks(b.Checkout)
	if err != nil {
		return err
	}
	if checkout != b.Checkout {
		return errors.New("prepared checkout location changed")
	}
	cwd, err := filepath.EvalSymlinks(b.Cwd)
	if err != nil {
		return err
	}
	info, err := os.Lstat(b.Checkout)
	if err != nil || !info.IsDir() {
		return errors.New("prepared checkout is unavailable or redirected")
	}
	relative, err := filepath.Rel(b.Checkout, b.Cwd)
	if err != nil || !filepath.IsLocal(relative) || cwd != filepath.Join(checkout, relative) {
		return errors.New("prepared checkout or cwd was redirected")
	}
	info, err = os.Stat(cwd)
	if err != nil || !info.IsDir() {
		return errors.New("prepared cwd is unavailable")
	}
	if b.VCS == "hg" {
		return validateHgIdentity(ctx, b)
	}
	if b.VCS != "" {
		return errors.New("unsupported checkout VCS")
	}
	root, err := git(ctx, cwd, "rev-parse", "--show-toplevel")
	if err != nil || root != checkout {
		return errors.New("prepared cwd no longer belongs to its checkout")
	}
	branch, err := git(ctx, cwd, "symbolic-ref", "--quiet", "HEAD")
	if err != nil || branch != "refs/heads/"+b.Branch {
		return errors.New("prepared checkout branch changed")
	}
	return nil
}

// RecordThread publishes host identity before the runtime may send turn/start.
// A late acknowledgement remains useful even when its MCP caller has canceled.
func (s *Store) RecordThread(ctx context.Context, workspace, main, name, thread string, result jsontext.Value) error {
	result, err := launchObject(result)
	if thread == "" || thread == main || err != nil {
		return errors.New("launch requires a separate host thread and response object")
	}
	return s.withBatch(ctx, workspace, main, name, func(b *Batch) error {
		if b.Launch == nil || b.State != "starting" && b.State != "started" {
			return errors.New("batch is not awaiting a thread acknowledgement")
		}
		if b.Launch.ThreadID != "" {
			if b.Launch.ThreadID != thread || !bytes.Equal(b.Launch.ThreadResult, result) {
				return errors.New("host thread acknowledgement changed")
			}
			return errLaunchUnchanged
		}
		b.Launch.ThreadID, b.Launch.ThreadResult = thread, result
		b.State = "started"
		return nil
	})
}

// Normalize object order and whitespace before comparing persisted JSON while
// preserving numeric precision in host configuration and results.
func launchObject(raw jsontext.Value) (jsontext.Value, error) {
	value := slices.Clone(raw)
	if err := value.Canonicalize(jsontext.CanonicalizeRawInts(false), jsontext.CanonicalizeRawFloats(false)); err != nil {
		return nil, err
	}
	if value.Kind() != '{' {
		return nil, errors.New("expected a JSON object")
	}
	return value, nil
}

// RecordTurn retains the acknowledgement, not a claim that the turn is still
// running. Live completion events are owned by the runtime coordinator.
func (s *Store) RecordTurn(ctx context.Context, workspace, main, name, thread, turn string) error {
	if turn == "" {
		return errors.New("launch requires a host turn identity")
	}
	return s.withBatch(ctx, workspace, main, name, func(b *Batch) error {
		if b.Launch == nil || b.Launch.ThreadID != thread || b.State != "started" && b.State != "launched" {
			return errors.New("batch is not awaiting a turn acknowledgement")
		}
		if b.Launch.TurnID != "" && b.Launch.TurnID != turn {
			return errors.New("host turn acknowledgement changed")
		}
		if b.Launch.TurnID == turn && b.State == "launched" {
			return errLaunchUnchanged
		}
		b.Launch.TurnID, b.State = turn, "launched"
		return nil
	})
}

func (s *Store) withBatch(ctx context.Context, workspace, main, name string, apply func(*Batch) error) error {
	return s.withRun(ctx, workspace, main, func(m *manifest, path string) error {
		for i := range m.Batches {
			if m.Batches[i].TaskName == name {
				if err := apply(&m.Batches[i]); err != nil {
					if errors.Is(err, errLaunchUnchanged) {
						return nil
					}
					return err
				}
				return s.save(m, path)
			}
		}
		return fmt.Errorf("batch %q has not been prepared", name)
	})
}

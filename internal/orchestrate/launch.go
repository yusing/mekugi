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
// serialized thread/start configuration; SandboxPolicy is the exact first-turn
// override when the host has no re-selectable profile. Both precede dispatch.
type Launch struct {
	Invocation     jsontext.Value `json:"invocation,omitempty"` // Original spawn input, independent of resolved settings.
	SandboxPolicy  jsontext.Value `json:"sandbox_policy,omitempty"`
	Message        string         `json:"message"`
	Params         jsontext.Value `json:"params"`
	ThreadID       string         `json:"thread_id,omitempty"`
	TurnID         string         `json:"turn_id,omitempty"`
	HostStatus     string         `json:"host_status,omitempty"`
	TurnUnsent     bool           `json:"turn_unsent,omitzero"`       // Canceled before first-turn dispatch.
	DeliveryTurnID string         `json:"delivery_turn_id,omitempty"` // Observed manual turn awaiting terminal reconciliation.
}

// CancelUnsentTurn records only a first turn which the caller has not dispatched.
// Missing acknowledgement alone must never authorize a retry.
func (s *Store) CancelUnsentTurn(ctx context.Context, workspace, main, name string) error {
	return s.withBatch(ctx, workspace, main, name, func(b *Batch) error {
		if b.Launch == nil || b.Launch.ThreadID == "" || b.Launch.TurnID != "" || b.Launch.HostStatus != "" {
			return errors.New("launch has no undispatched first turn")
		}
		b.Launch.TurnUnsent = true
		return nil
	})
}

var errLaunchUnchanged = errors.New("launch unchanged")

// ObserveLaunch retains host outcomes independently of launch acknowledgements,
// which may arrive after a turn's lifecycle notifications.
func (s *Store) ObserveLaunch(ctx context.Context, workspace, main, name, status, message string) error {
	return s.withBatch(ctx, workspace, main, name, func(b *Batch) error {
		if b.Launch == nil {
			return errors.New("batch has no launch intent")
		}
		if b.Launch.HostStatus == status && b.Error == message && !b.Launch.TurnUnsent {
			return errLaunchUnchanged
		}
		b.Launch.TurnUnsent = false
		b.Launch.HostStatus, b.Error = status, message
		return nil
	})
}

// BeginLaunch reserves dispatch once. Only a true dispatch result authorizes
// thread/start; repeats return retained facts, including uncertain outcomes.
func (s *Store) BeginLaunch(ctx context.Context, workspace, main, name, message string, params, policy, invocation jsontext.Value) (batch Batch, dispatch bool, err error) {
	params, err = launchObject(params)
	if err == nil && len(policy) != 0 {
		policy, err = launchObject(policy)
	}
	if err == nil && len(invocation) != 0 {
		invocation, err = launchObject(invocation)
	}
	if message == "" || err != nil {
		return batch, false, errors.New("launch requires a handoff and host configuration object")
	}
	err = s.withBatch(ctx, workspace, main, name, func(b *Batch) error {
		batch = *b
		if b.Launch != nil {
			if !bytes.Equal(b.Launch.Invocation, invocation) || b.Launch.Message != message || !bytes.Equal(b.Launch.Params, params) || !bytes.Equal(b.Launch.SandboxPolicy, policy) {
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
		for _, evidence := range b.Evidence {
			if err := validateEvidence(evidence); err != nil {
				return err
			}
		}
		b.Launch = &Launch{Message: message, Params: params, SandboxPolicy: policy, Invocation: invocation}
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
	if info, err := os.Stat(b.Cwd); err != nil || !info.IsDir() {
		return errors.New("prepared cwd is unavailable")
	}
	base, err := sourceBase(ctx, b.Cwd)
	if err != nil || base != b.Base {
		return errors.New("prepared checkout baseline changed")
	}
	return validateSubmodules(ctx, b, true)
}

func validateCheckoutIdentity(ctx context.Context, b Batch) error {
	checkout, err := filepath.EvalSymlinks(b.Checkout)
	if err != nil {
		return err
	}
	if checkout != b.Checkout {
		return errors.New("prepared checkout location changed")
	}
	cwd, err := existingDirectory(b.Cwd)
	if err != nil {
		return err
	}
	info, err := os.Lstat(b.Checkout)
	if err != nil || !info.IsDir() {
		return errors.New("prepared checkout is unavailable or redirected")
	}
	relative, err := filepath.Rel(b.Checkout, b.Cwd)
	if err != nil || !filepath.IsLocal(relative) {
		return errors.New("prepared checkout or cwd was redirected")
	}
	resolved, err := filepath.EvalSymlinks(cwd)
	if err != nil || resolved != cwd {
		return errors.New("prepared checkout or cwd was redirected")
	}
	if b.VCS != "" && !shadowVCS(b.VCS) {
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
	if shadowVCS(b.VCS) {
		common, err := git(ctx, cwd, "rev-parse", "--path-format=absolute", "--git-common-dir")
		if err != nil || common != b.Repository {
			return errors.New("shadow checkout repository changed")
		}
	}
	return nil
}

// RecordThread publishes host identity before the runtime may send turn/start.
// A late acknowledgement remains useful even when its MCP caller has canceled.
func (s *Store) RecordThread(ctx context.Context, workspace, main, name, thread string) error {
	if thread == "" || thread == main {
		return errors.New("launch requires a separate host thread")
	}
	return s.withBatch(ctx, workspace, main, name, func(b *Batch) error {
		if b.Launch == nil || b.Launch.TurnID != "" || b.State == "removing" || b.State == "removed" {
			return errors.New("batch is not awaiting a thread acknowledgement")
		}
		if b.Launch.ThreadID != "" {
			if b.Launch.ThreadID != thread {
				return errors.New("host thread acknowledgement changed")
			}
			return errLaunchUnchanged
		}
		b.Launch.ThreadID = thread
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
		if b.Launch == nil || b.Launch.ThreadID == "" || b.Launch.ThreadID != thread || b.State == "removing" || b.State == "removed" {
			return errors.New("batch is not awaiting a turn acknowledgement")
		}
		if b.Launch.TurnID != "" && b.Launch.TurnID != turn {
			return errors.New("host turn acknowledgement changed")
		}
		if b.Launch.TurnID == turn {
			return errLaunchUnchanged
		}
		b.Launch.TurnID, b.Launch.TurnUnsent = turn, false
		return nil
	})
}

func (s *Store) withBatch(ctx context.Context, workspace, main, name string, apply func(*Batch) error) error {
	return s.withBatchRun(ctx, workspace, main, name, func(m *manifest, path string) error {
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

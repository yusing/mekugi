package router

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"strings"
	"time"

	"github.com/yusing/mekugi/internal/hpatchsyntax"
)

const maxHpatchCheckpointBytes = 32 << 20

//go:embed hpatch_resume.js
var hpatchResumeRuntime string

type hpatchResumeSegment struct {
	Source  string `json:"source"`
	Program string `json:"program"`
	Line    int    `json:"line"`
	Kind    string `json:"kind"`
}

type hpatchResumeState struct {
	ChangeID        string             `json:"change_id,omitempty"`
	CorrelationID   string             `json:"correlation_id,omitempty"`
	LiveDiff        liveDiffConnection `json:"live_diff,omitzero"`
	liveDiff        func([]liveDiffChange)
	ReplayDirectory string                     `json:"replay_directory,omitempty"`
	Handle          string                     `json:"handle"`
	Root            string                     `json:"root"`
	Source          string                     `json:"source"`
	Segments        []hpatchResumeSegment      `json:"segments"`
	ExpiresAt       time.Time                  `json:"expires_at"`
	Revision        uint64                     `json:"revision"`
	Progress        map[string]json.RawMessage `json:"progress"`
}

func mixedArtifactName(handle string) (string, error) {
	if len(handle) != 33 || handle[0] != 'M' {
		return "", errors.New("invalid mixed-script resume handle")
	}
	if _, err := hex.DecodeString(handle[1:]); err != nil || strings.ToLower(handle[1:]) != handle[1:] {
		return "", errors.New("invalid mixed-script resume handle")
	}
	return "mixed-" + handle, nil
}

func (t *mekugiResponseTransform) retainMixedScript(changeID, correlationID, source string, segments []hpatchResumeSegment) (hpatchResumeState, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return hpatchResumeState{}, err
	}
	state := hpatchResumeState{
		ChangeID: changeID, CorrelationID: correlationID,
		Handle: "M" + hex.EncodeToString(nonce[:]), Root: t.directory, ExpiresAt: time.Now().Add(shellArtifactTTL),
		Source: source, Segments: segments,
		Progress: map[string]json.RawMessage{
			"index": mustMarshalJSON(0), "results": mustMarshalJSON([]any{}),
			"operations": mustMarshalJSON([]any{}),
		},
	}
	if t.proxy.replayStore != nil {
		state.ReplayDirectory = t.proxy.replayStore.directory
	}
	if t.proxy.autoLiveDiff != nil && t.proxy.autoLiveDiff.enabled.Load() && state.ChangeID != "" {
		state.LiveDiff = t.proxy.autoLiveDiff.events.expectProducer(state.Root, t.shellThreadID, state.ChangeID, state.Handle)
	}
	name, _ := mixedArtifactName(state.Handle)
	if _, _, ok := t.proxy.retainShell(t.shellDirectory, name, string(mustMarshalJSON(state))); !ok {
		return hpatchResumeState{}, errors.New("mixed-script retention is unavailable; no segment ran")
	}
	return state, nil
}

func (t *mekugiResponseTransform) translateMixedResume(callID, input string, upstream map[string]json.RawMessage) (mekugiHistory, error) {
	history := mekugiHistory{ToolName: mekugiToolName, Script: input, Root: t.directory,
		CarrierName: t.codeModeToolName, UpstreamItem: maps.Clone(upstream)}
	reject := func(err error) (mekugiHistory, error) {
		history.TranslationError = changeNotice(history.ChangeID) + "hpatch resume: " + err.Error()
		t.recordLocal(callID, &history)
		return history, nil
	}
	if t.nativeTools {
		return reject(errors.New("mixed-script continuation requires Code Mode"))
	}
	header, replacement, _ := strings.Cut(strings.TrimLeft(input, " \t\r\n"), "\n")
	fields := strings.Fields(header)
	if len(fields) < 2 || len(fields) > 3 || fields[0] != "resume" {
		return reject(errors.New("expected resume HANDLE [retry|accept|repair], with one edit segment for repair or an optional replacement for retry"))
	}
	action := ""
	if len(fields) == 3 {
		action = fields[2]
	}
	name, err := mixedArtifactName(fields[1])
	if err != nil {
		return reject(err)
	}
	root, release, err := t.proxy.shellRoot(t.shellDirectory)
	if err != nil {
		return reject(errors.New("resume handle is unavailable in this thread"))
	}
	defer release()
	file, err := openRegularShellFile(root, name)
	if err != nil {
		return reject(errors.New("resume handle is unavailable or expired"))
	}
	defer file.Close()
	var state hpatchResumeState
	if err := json.NewDecoder(io.LimitReader(file, maxHpatchCheckpointBytes+1)).Decode(&state); err != nil ||
		state.Handle != fields[1] || state.Root != t.directory || !time.Now().Before(state.ExpiresAt) {
		return reject(errors.New("resume handle does not identify valid work in this workspace"))
	}
	history.ChangeID, history.CorrelationID = state.ChangeID, state.CorrelationID
	history.Attempt = 1
	if action != "" && action != "retry" && action != "accept" && action != "repair" {
		return reject(errors.New("resume action must be retry, accept, or repair"))
	}
	var changed *hpatchResumeSegment
	if strings.TrimSpace(replacement) != "" {
		if action != "retry" && action != "repair" {
			return reject(errors.New("a segment requires retry or repair"))
		}
		parts, _, err := hpatchsyntax.SplitShell(replacement)
		if err != nil {
			return reject(err)
		}
		if len(parts) != 1 {
			return reject(errors.New("supply one replacement or repair segment, not the unchanged suffix"))
		}
		segments, err := t.prepareMixedSegments(parts)
		if err != nil {
			return reject(err)
		}
		if action == "repair" && segments[0].Kind != "edit" {
			return reject(errors.New("repair requires one workspace edit segment"))
		}
		changed = &segments[0]
	}
	if action == "repair" && changed == nil {
		return reject(errors.New("repair requires one workspace edit segment"))
	}
	if t.proxy.autoLiveDiff != nil && t.proxy.autoLiveDiff.enabled.Load() && state.ChangeID != "" {
		t.proxy.autoLiveDiff.events.resumeProducer(state.Root, t.shellThreadID, state.ChangeID, state.Handle, state.LiveDiff)
	}
	history.CarrierKind = codeModeCarrierCustom
	history.CarrierPayload = t.mixedCarrier(state, action, changed)
	t.recordLocal(callID, &history)
	return history, nil
}

func (t *mekugiResponseTransform) mixedCarrier(state hpatchResumeState, action string, replacement *hpatchResumeSegment) string {
	config := map[string]any{
		"state": map[string]any{
			"change_id": state.ChangeID, "expires_at": state.ExpiresAt,
			"handle": state.Handle, "progress": state.Progress,
			"revision": state.Revision, "segments": state.Segments,
		},
		"action": action, "replacement": replacement,
	}
	return "const mixedConfig = " + string(mustMarshalJSON(config)) + ";\n" + hpatchResumeRuntime
}

// Checkpoint storage is independent of the Code Mode isolate: store() commits
// only when its cell completes and cannot preserve hard-cancellation progress.
// The worker changes private runtime state only, never workspace files.
func runHpatchCheckpoint(ctx context.Context, root *os.Root, handle string, revision uint64, payload string, stdout io.Writer) error {
	if len(payload) > maxHpatchCheckpointBytes {
		return errors.New("checkpoint exceeds retention limit")
	}
	var progress map[string]json.RawMessage
	if err := json.Unmarshal([]byte(payload), &progress); err != nil || progress == nil {
		return errors.New("invalid checkpoint state")
	}
	name, err := mixedArtifactName(handle)
	if err != nil {
		return err
	}
	unlock, err := lockHpatchCheckpoint(root, name)
	if err != nil {
		return err
	}
	defer unlock()
	file, err := openRegularShellFile(root, name)
	if err != nil {
		return errors.New("checkpoint expired or unavailable")
	}
	var state hpatchResumeState
	err = json.NewDecoder(io.LimitReader(file, maxHpatchCheckpointBytes+1)).Decode(&state)
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		return errors.Join(err, closeErr)
	}
	if state.Handle != strings.TrimPrefix(name, "mixed-") || state.Revision != revision || !time.Now().Before(state.ExpiresAt) {
		return errors.New("stale continuation carrier; no further operation may start")
	}
	state.Progress = progress
	state.Revision++
	encoded, err := json.Marshal(state)
	if err != nil || len(encoded) > maxHpatchCheckpointBytes {
		return errors.New("checkpoint exceeds retention limit")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	temporary := name + "-" + hex.EncodeToString(nonce[:]) + ".tmp"
	pending, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer root.Remove(temporary)
	_, writeErr := pending.Write(encoded)
	err = errors.Join(writeErr, pending.Sync(), pending.Close())
	if err != nil {
		return err
	}
	if err := root.Rename(temporary, name); err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "%d\n", state.Revision)
	return err
}

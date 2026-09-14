package router

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/yusing/mekugi/internal/shellruntime"
	"golang.org/x/term"
)

const hpatchTranslationReady = "HPATCH-READY\n"

type hpatchControlMutation struct {
	Path   []string        `json:"path"`
	Value  json.RawMessage `json:"value,omitempty"`
	Remove bool            `json:"remove,omitempty"`
	Copy   string          `json:"copy,omitempty"`
}

type hpatchControlRequest struct {
	AttemptID string                  `json:"attempt_id"`
	Operation string                  `json:"operation"`
	Handle    string                  `json:"handle"`
	Revision  uint64                  `json:"revision"`
	Mutations []hpatchControlMutation `json:"mutations"`
	Source    string                  `json:"source"`
}

const maxHpatchControlMutations = 4096

func applyHpatchControlMutations(progress map[string]json.RawMessage, mutations []hpatchControlMutation, translation json.RawMessage) (map[string]json.RawMessage, error) {
	if len(mutations) > maxHpatchControlMutations {
		return nil, errors.New("too many checkpoint mutations")
	}
	encoded, err := json.Marshal(progress)
	if err != nil {
		return nil, errors.New("invalid retained checkpoint")
	}
	var document any
	if err := json.Unmarshal(encoded, &document); err != nil {
		return nil, errors.New("invalid retained checkpoint")
	}
	copiedTranslation := false
	for _, mutation := range mutations {
		if len(mutation.Path) == 0 || len(mutation.Path) > 64 {
			return nil, errors.New("invalid checkpoint mutation path")
		}
		value, err := hpatchControlMutationValue(mutation, translation)
		if err != nil {
			return nil, err
		}
		if mutation.Copy != "" {
			if copiedTranslation {
				return nil, errors.New("translation result copied more than once")
			}
			copiedTranslation = true
		}
		document, err = applyHpatchControlMutation(document, mutation.Path, value, mutation.Remove)
		if err != nil {
			return nil, err
		}
	}
	encoded, err = json.Marshal(document)
	if err != nil {
		return nil, errors.New("invalid checkpoint mutation")
	}
	var updated map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &updated); err != nil || updated == nil {
		return nil, errors.New("checkpoint mutations produced invalid state")
	}
	return updated, nil
}

func hpatchControlMutationValue(mutation hpatchControlMutation, translation json.RawMessage) (any, error) {
	if mutation.Remove {
		if len(mutation.Value) != 0 || mutation.Copy != "" {
			return nil, errors.New("checkpoint removal has a value")
		}
		return nil, nil
	}
	if mutation.Copy != "" {
		if mutation.Copy != "translation" || len(translation) == 0 || len(mutation.Value) != 0 {
			return nil, errors.New("invalid checkpoint copy")
		}
		return map[string]any{"output": string(translation), "exit_code": 0}, nil
	}
	if len(mutation.Value) == 0 {
		return nil, errors.New("checkpoint mutation has no value")
	}
	var value any
	if err := json.Unmarshal(mutation.Value, &value); err != nil {
		return nil, errors.New("invalid checkpoint mutation value")
	}
	return value, nil
}

func applyHpatchControlMutation(node any, path []string, value any, remove bool) (any, error) {
	component := path[0]
	last := len(path) == 1
	switch current := node.(type) {
	case map[string]any:
		if last {
			if remove {
				if _, ok := current[component]; !ok {
					return nil, errors.New("checkpoint mutation removes a missing field")
				}
				delete(current, component)
			} else {
				current[component] = value
			}
			return current, nil
		}
		child, ok := current[component]
		if !ok {
			return nil, errors.New("checkpoint mutation parent is missing")
		}
		updated, err := applyHpatchControlMutation(child, path[1:], value, remove)
		if err != nil {
			return nil, err
		}
		current[component] = updated
		return current, nil
	case []any:
		index, err := strconv.Atoi(component)
		if err != nil || index < 0 || index > len(current) || !last && index == len(current) {
			return nil, errors.New("invalid checkpoint mutation index")
		}
		if last {
			if remove {
				if index == len(current) {
					return nil, errors.New("checkpoint mutation removes a missing item")
				}
				return append(current[:index], current[index+1:]...), nil
			}
			if index == len(current) {
				return append(current, value), nil
			}
			current[index] = value
			return current, nil
		}
		updated, err := applyHpatchControlMutation(current[index], path[1:], value, remove)
		if err != nil {
			return nil, err
		}
		current[index] = updated
		return current, nil
	default:
		return nil, errors.New("checkpoint mutation traverses a scalar")
	}
}

// A bare shell starts a private, stdin-framed control channel. Neither runtime
// paths nor control payloads are command arguments. Each channel binds exactly
// one retained handle inside the inherited thread's storage, never a global
// "current script", so concurrent carriers cannot borrow each other's context.
func runHpatchControl(ctx context.Context, stdin *os.File, stdout io.Writer) error {
	directory, err := shellruntime.Directory()
	if err != nil {
		return err
	}
	return runHpatchControlAt(ctx, stdin, stdout, directory, os.Getenv(shellruntime.ThreadIDEnvironment))
}

func runHpatchControlAt(
	ctx context.Context,
	stdin *os.File,
	stdout io.Writer,
	directory, threadID string,
) error {
	path, err := shellruntime.ScriptsPath(directory, threadID)
	if err != nil {
		return err
	}
	parent, err := os.OpenRoot(directory)
	if err != nil {
		return err
	}
	defer parent.Close()
	root, err := openExistingShellDirectory(parent, filepath.Base(path))
	if err != nil {
		return err
	}
	defer root.Close()
	if stdin == nil {
		return errors.New("control channel requires stdin")
	}
	if term.IsTerminal(int(stdin.Fd())) {
		state, err := term.MakeRaw(int(stdin.Fd()))
		if err != nil {
			return err
		}
		defer term.Restore(int(stdin.Fd()), state)
	}
	input, closeInput, err := openHpatchInput(stdin)
	if err != nil {
		return err
	}
	defer closeInput()
	// PTY stdin/stdout may share an open-file description. Making input
	// nonblocking also changes stdout; wrap a duplicate in Go's poller so a
	// large reply waits for writable capacity instead of failing with EAGAIN.
	if file, ok := stdout.(*os.File); ok {
		output, closeOutput, err := openHpatchInput(file)
		if err != nil {
			return err
		}
		defer closeOutput()
		stopOutput := context.AfterFunc(ctx, func() { _ = output.Close() })
		defer stopOutput()
		stdout = output
	}
	stop := context.AfterFunc(ctx, func() { _ = input.Close() })
	defer stop()
	// An abandoned startup expires promptly; a bound channel never outlives
	// its retained handle, even when hard cancellation skips carrier cleanup.
	if err := input.SetReadDeadline(time.Now().Add(time.Minute)); err != nil {
		if errors.Is(err, os.ErrNoDeadline) {
			return errors.New("unsupported stdin: shell without arguments requires a pipe or terminal for its internal control channel; use functions.shell to run scripts")
		}
		return err
	}
	if _, err := io.WriteString(stdout, hpatchTranslationReady); err != nil {
		return err
	}
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), maxHpatchCheckpointBytes+1)
	var livePublisher *liveDiffProducer
	defer func() { livePublisher.close() }()
	var bound hpatchResumeState
	var translation json.RawMessage
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		var request hpatchControlRequest
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			return errors.New("invalid control frame")
		}
		if request.Operation == "close" {
			return nil
		}
		var response any
		if bound.Handle == "" {
			if request.Operation != "open" {
				return errors.New("control channel must bind a handle first")
			}
			name, err := mixedArtifactName(request.Handle)
			if err != nil {
				return err
			}
			file, err := openRegularShellFile(root, name)
			if err != nil {
				return errors.New("control handle unavailable in this thread")
			}
			err = json.NewDecoder(io.LimitReader(file, maxHpatchCheckpointBytes+1)).Decode(&bound)
			closeErr := file.Close()
			if err != nil || closeErr != nil || bound.Handle != request.Handle || !time.Now().Before(bound.ExpiresAt) {
				return errors.New("control handle invalid or expired")
			}
			if err := input.SetReadDeadline(bound.ExpiresAt); err != nil {
				return err
			}
			livePublisher = startLiveDiffProducer(ctx, bound.LiveDiff)
			bound.liveDiff = livePublisher.publish
			response = map[string]any{"opened": true, "progress": bound.Progress}
		} else {
			if !time.Now().Before(bound.ExpiresAt) {
				return errors.New("control handle expired")
			}
			switch request.Operation {
			case "checkpoint":
				updated, err := applyHpatchControlMutations(bound.Progress, request.Mutations, translation)
				if err != nil {
					return err
				}
				progress, err := json.Marshal(updated)
				if err != nil {
					return errors.New("invalid checkpoint state")
				}
				if err := runHpatchCheckpoint(ctx, root, bound.Handle, request.Revision, string(progress), io.Discard); err != nil {
					return err
				}
				if err := json.Unmarshal(progress, &bound.Progress); err != nil {
					return errors.New("invalid checkpoint state")
				}
				bound.Revision = request.Revision + 1
				translation = nil
				response = map[string]any{"revision": bound.Revision}
			case "translate":
				translated, err := bound.translateTracked(ctx, request.Source)
				if err != nil {
					return err
				}
				translation, err = json.Marshal(translated)
				if err != nil {
					return errors.New("invalid translation result")
				}
				response = translated
			case "confirm":
				if err := bound.confirmTracked(ctx, request.AttemptID); err != nil {
					return err
				}
				response = map[string]any{"confirmed": true}
			default:
				return fmt.Errorf("invalid control operation")
			}
		}
		encoded, err := json.Marshal(response)
		if err != nil || len(encoded) > maxHpatchCheckpointBytes {
			return errors.New("control response exceeds retention limit")
		}
		// Explicit acknowledgement keeps each native result well below the
		// host's output/token limits; writable PTY capacity alone is not enough.
		for len(encoded) != 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
			if !time.Now().Before(bound.ExpiresAt) {
				return errors.New("control handle expired")
			}
			size := min(len(encoded), 16<<10)
			for !utf8.Valid(encoded[:size]) {
				size--
			}
			frame := struct {
				Data string `json:"data"`
				More bool   `json:"more"`
			}{string(encoded[:size]), size < len(encoded)}
			if file, ok := stdout.(*os.File); ok {
				deadline := time.Now().Add(time.Minute)
				if bound.ExpiresAt.Before(deadline) {
					deadline = bound.ExpiresAt
				}
				if err := file.SetWriteDeadline(deadline); err != nil {
					return err
				}
			}
			if err := json.NewEncoder(stdout).Encode(frame); err != nil {
				return err
			}
			encoded = encoded[size:]
			if frame.More {
				if !scanner.Scan() {
					return scanner.Err()
				}
				var next hpatchControlRequest
				if err := json.Unmarshal(scanner.Bytes(), &next); err != nil {
					return errors.New("invalid control acknowledgement")
				}
				if next.Operation == "close" {
					return nil
				}
				if next.Operation != "next" {
					return errors.New("control response requires acknowledgement")
				}
			}
		}
	}
	return scanner.Err()
}

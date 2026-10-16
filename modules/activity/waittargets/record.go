// Package waittargets preserves immutable start-time roster snapshots for waits.
// These records describe presentation evidence, never resumable host operations.
package waittargets

import (
	"crypto/sha256"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/yusing/mekugi/contracts/recordkind"
)

const maxBytes = 64 << 10

// Source: apps/mekugi/internal/router/app_server_wait_targets.go:17:20@[29de53cc] appServerWaitTarget
type Target struct {
	Thread string `json:"thread"`
	Name   string `json:"name"`
}

// Name binds the caller's proven workspace and host item identity.
// Source: apps/mekugi/internal/router/app_server_wait_targets.go:123:126@[29de53cc] waitTargetsPath
func Name(workspace, thread, turn, item string) string {
	identity := workspace + "\x00" + thread + "\x00" + turn + "\x00" + item
	return fmt.Sprintf("wait-%x.json", sha256.Sum256([]byte(identity)))
}

// Record enrolls snapshots in ordinary session retention without a separate store.
// Source: apps/mekugi/internal/router/retained_record_kinds.go:23:50@[29de53cc] retained wait identity
var Record = recordkind.Spec{ID: "wait", Match: func(name string) bool {
	hash, ok := strings.CutPrefix(name, "wait-")
	if !ok {
		return false
	}
	hash, ok = strings.CutSuffix(hash, ".json")
	return ok && len(hash) == 64 && strings.Trim(hash, "0123456789abcdef") == ""
}}

// Read validates one bounded regular file without creating storage or ownership.
// Source: apps/mekugi/internal/router/app_server_wait_targets.go:128:166@[29de53cc] readWaitTargets
func Read(path string) ([]Target, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !info.Mode().IsRegular() {
		return nil, false, errors.New("wait targets are not a regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, false, err
	}
	if len(data) > maxBytes {
		return nil, false, errors.New("wait targets exceed size limit")
	}
	var targets []Target
	if err := json.Unmarshal(data, &targets, json.RejectUnknownMembers(true)); err != nil {
		return nil, false, err
	}
	return targets, true, nil
}

// Encode preserves the retained target order and JSON encoding.
// Source: apps/mekugi/internal/router/app_server_wait_targets.go:168:182@[29de53cc] writeWaitTargets
func Encode(targets []Target) ([]byte, error) {
	data, err := json.Marshal(&targets)
	if err != nil {
		return nil, err
	}
	if len(data) > maxBytes {
		return nil, errors.New("wait targets exceed size limit")
	}
	return data, nil
}

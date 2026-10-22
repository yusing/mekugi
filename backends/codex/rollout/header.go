// Source: internal/router/debug_ax.go:181:208@[ac8511fa] debugRolloutIdentity.
package rollout

import (
	"bufio"
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/yusing/mekugi/libraries/sourceanalysis/sourceparse"
)

// Read only the bounded metadata header during discovery. Full rollout validation
// remains with the inspector; unreadable candidates cannot certify uniqueness.
func HeaderIdentity(path string) (string, error) {
	header, err := ReadHeader(path)
	return header.ID, err
}

// Header is the identity and workspace recorded before any rollout effects.
type Header struct {
	ID        string `json:"id"`
	Workspace string `json:"cwd"`
}

// HomeDirectory resolves the host's session/configuration root.
func HomeDirectory() (string, error) {
	if directory := os.Getenv("CODEX_HOME"); directory != "" {
		return directory, nil
	}
	directory, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(directory, ".codex"), nil
}

// ReadHeader reads one bounded metadata record without validating later effects.
func ReadHeader(path string) (Header, error) {
	file, err := sourceparse.Open(path)
	if err != nil {
		return Header{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return Header{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxInspectionBytes {
		return Header{}, errors.New("rollout must be a bounded regular file")
	}
	scanner := bufio.NewScanner(io.LimitReader(file, maxInspectionLineBytes+1))
	scanner.Buffer(make([]byte, 4096), maxInspectionLineBytes)
	var metadata struct {
		Type    string `json:"type"`
		Payload Header `json:"payload"`
	}
	if !scanner.Scan() || json.Unmarshal(scanner.Bytes(), &metadata, jsonv1.DefaultOptionsV1()) != nil ||
		metadata.Type != "session_meta" || metadata.Payload.ID == "" {
		return Header{}, errors.New("rollout metadata is unavailable or invalid")
	}
	return metadata.Payload, nil
}

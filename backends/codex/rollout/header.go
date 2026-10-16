// Source: internal/router/debug_ax.go:181:208@[ac8511fa] debugRolloutIdentity.
package rollout

import (
	"bufio"
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"errors"
	"io"

	"github.com/yusing/mekugi/libraries/sourceanalysis/sourceparse"
)

// Read only the bounded metadata header during discovery. Full rollout validation
// remains with the inspector; unreadable candidates cannot certify uniqueness.
func HeaderIdentity(path string) (string, error) {
	file, err := sourceparse.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > maxInspectionBytes {
		return "", errors.New("rollout must be a bounded regular file")
	}
	scanner := bufio.NewScanner(io.LimitReader(file, maxInspectionLineBytes+1))
	scanner.Buffer(make([]byte, 4096), maxInspectionLineBytes)
	var metadata struct {
		Type    string `json:"type"`
		Payload struct {
			ID string `json:"id"`
		} `json:"payload"`
	}
	if !scanner.Scan() || json.Unmarshal(scanner.Bytes(), &metadata, jsonv1.DefaultOptionsV1()) != nil ||
		metadata.Type != "session_meta" || metadata.Payload.ID == "" {
		return "", errors.New("rollout metadata is unavailable or invalid")
	}
	return metadata.Payload.ID, nil
}

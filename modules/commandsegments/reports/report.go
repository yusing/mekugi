// Package reports owns completed command-segment evidence, not live processes.
package reports

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Source: internal/router/command_segments.go:21:38@[cc2524a0] retained reports and identity.
type Report struct {
	Command string
	Exit    int
	Output  [32]byte // Digest of the exact host aggregate, without a second copy.
	Parts   []Part
}

type Part struct {
	Timing  Timing `json:",omitzero"`
	Source  string
	Skipped bool
	Exit    int
	Output  *string // nil means unavailable; an empty string is complete empty output.
}

type Timing struct {
	Started   time.Time `json:"started,omitzero"`
	Ended     time.Time `json:"ended,omitzero"`
	ElapsedNS int64     `json:"elapsed_ns,omitzero"`
}

// ID remains stable across forked history with the same host turn and item.
func ID(turn, item string) string {
	return fmt.Sprintf("command-segments:%x", sha256.Sum256(fmt.Appendf(nil, "%q:%q", turn, item)))
}

func IsID(id string) bool {
	hash, ok := strings.CutPrefix(id, "command-segments:")
	return ok && validHash(hash)
}

func validHash(hash string) bool {
	if len(hash) != 64 || strings.ToLower(hash) != hash {
		return false
	}
	_, err := hex.DecodeString(hash)
	return err == nil
}

// Source: internal/router/command_segments.go:220:247@[cc2524a0] report validation.
func (r Report) validate() error {
	if len(r.Parts) == 0 {
		return errors.New("command segment report has no segments")
	}
	for _, part := range r.Parts {
		timing := part.Timing
		if part.Skipped && (part.Output != nil || part.Exit != 0 || timing != (Timing{})) ||
			timing.ElapsedNS < 0 || timing.Started.IsZero() && (!timing.Ended.IsZero() || timing.ElapsedNS != 0) || timing.Ended.IsZero() && timing.ElapsedNS != 0 {
			return errors.New("invalid command segment state or timing")
		}
	}
	return nil
}

func (r Report) equal(other Report) bool {
	return r.Command == other.Command && r.Exit == other.Exit && r.Output == other.Output && slices.EqualFunc(r.Parts, other.Parts, func(a, b Part) bool {
		return a.Source == b.Source && a.Skipped == b.Skipped && a.Exit == b.Exit &&
			(a.Output == nil && b.Output == nil || a.Output != nil && b.Output != nil && *a.Output == *b.Output) &&
			a.Timing.Started.Equal(b.Timing.Started) && a.Timing.Ended.Equal(b.Timing.Ended) && a.Timing.ElapsedNS == b.Timing.ElapsedNS
	})
}

// Completed contains only terminal host facts and the original ordered sources.
// The caller decodes the host's shell representation before requesting restoration.
type Completed struct {
	ID      string
	Command string
	Exit    int
	Output  [32]byte
	Sources []string
}

func (c Completed) matches(r Report) bool {
	if c.Command != r.Command || c.Exit != r.Exit || c.Output != r.Output || len(c.Sources) != len(r.Parts) {
		return false
	}
	for i, source := range c.Sources {
		if source != r.Parts[i].Source {
			return false
		}
	}
	return true
}

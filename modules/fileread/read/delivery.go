package read

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/libraries/outputbudget/tokenizer"
)

// Source: internal/router/shell_read_bundle.go:292:366@[4b6a7a16] bundle framing.
type bundleDelivery struct {
	entries []readBundleEntry
	budget  int
	output  command.Output
}

func (p *bundleDelivery) ReferenceCount() int { return len(p.entries) }

func (p *bundleDelivery) Prepare(references []string) (command.PreparedDelivery, error) {
	if len(references) != p.ReferenceCount() {
		return command.PreparedDelivery{}, errors.New("reference count does not match read bundle")
	}
	return p.prepare(references)
}

// A nil reference list prepares bounded fallback output before reservation.
func (p *bundleDelivery) prepare(references []string) (command.PreparedDelivery, error) {
	entries := slices.Clone(p.entries)
	output := p.output
	codec, err := tokenizer.New()
	if err != nil {
		return command.PreparedDelivery{}, err
	}
	continuation := func() string {
		var handles []string
		for i, entry := range entries {
			if len(references) != 0 && (entry.recovery.Stdout != "" || entry.recovery.Stderr != "") {
				handles = append(handles, references[i])
			}
		}
		if len(handles) == 0 {
			return ""
		}
		text := "\nnext_call: mread " + strings.Join(handles, " ")
		if p.budget != 6000 {
			text += fmt.Sprintf(" --max-tokens %d", p.budget)
		}
		return text + "\n"
	}
	for {
		output.Stdout = renderReadBundle(entries)
		used, err := codec.Count(output.Stdout + continuation())
		if err != nil {
			return command.PreparedDelivery{}, err
		}
		if used <= p.budget {
			break
		}
		trimmed := false
		for i := len(entries) - 1; i >= 0; i-- {
			entry := &entries[i]
			if entry.shown == "" {
				continue
			}
			end := strings.LastIndex(entry.shown[:len(entry.shown)-1], "\n") + 1
			entry.recovery.Stdout = entry.shown[end:] + entry.recovery.Stdout
			entry.recovery.StdoutKind = "rows"
			entry.shown = entry.shown[:end]
			entry.recovery.SourceRow = entry.start + bundleRowCount(entry.shown)
			entry.omitted = bundleRowSpan(entry.recovery.SourceRow, entry.recovery.Stdout)
			entry.state, entry.recovery.ExitCode = "incomplete", 1
			output.ExitCode, output.FailureClass, trimmed = 1, "output_limit", true
			break
		}
		if !trimmed {
			failed := failure(errors.New("budget cannot fit the bundle manifest; increase --max-tokens or select fewer paths"), "invalid_arguments")
			return command.PreparedDelivery{Referenced: failed, Unreferenced: failed}, nil
		}
	}
	prepared := command.PreparedDelivery{Unreferenced: output, Referenced: output}
	prepared.Referenced.Stdout += continuation()
	for _, entry := range entries {
		if entry.recovery.Stdout != "" || entry.recovery.Stderr != "" {
			prepared.Recovery = append(prepared.Recovery, entry.recovery)
		}
	}
	return prepared, nil
}

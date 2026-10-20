// Source: apps/mekugi/internal/router/session_corpus.go@45b7fe8d
package inspectioncli

import (
	"context"
	jsonv1 "encoding/json"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"time"

	history "github.com/yusing/mekugi/modules/sessionhistory/history"

	"github.com/yusing/mekugi/modules/sessioninspection/query"
)

// RunSessionCorpusInspection is read-only. Counts are per rollout: inherited
// calls in forks are not summed into an invented global workload.
func RunSessionCorpusInspection(inspector *query.Service, ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("inspect-sessions", flag.ContinueOnError)
	flags.SetOutput(stderr)
	directory := flags.String("sessions-dir", "", "Codex rollout directory (default CODEX_HOME/sessions)")
	replay := flags.String("replay-dir", "", "read-only replay store override")
	sinceText := flags.String("since", time.Now().UTC().Add(-48*time.Hour).Format(time.RFC3339Nano), "inclusive RFC3339 start")
	untilText := flags.String("until", time.Now().UTC().Format(time.RFC3339Nano), "exclusive RFC3339 end")
	model := flags.String("model", "*", "model glob, applied to calls")
	exclude := flags.String("exclude-model", "", "exclude entire rollouts containing this model glob")
	class := flags.String("class", "all", "all, production, probe, or unknown (metadata-based candidates)")
	limit := flags.Int("limit", 25, "maximum findings per session (1–500)")
	capture := flags.String("capture", "", "optional sanitized provider capture JSONL")
	flags.Usage = func() { fmt.Fprintln(stderr, "Usage: mekugi inspect-sessions [options]"); flags.PrintDefaults() }
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	since, err1 := time.Parse(time.RFC3339Nano, *sinceText)
	until, err2 := time.Parse(time.RFC3339Nano, *untilText)
	_, err3 := filepath.Match(*model, "")
	_, err4 := filepath.Match(*exclude, "")
	if flags.NArg() != 0 || err1 != nil || err2 != nil || !since.Before(until) || err3 != nil || err4 != nil ||
		*limit < 1 || *limit > 500 || !slices.Contains([]string{"all", "production", "probe", "unknown"}, *class) {
		flags.Usage()
		return 2
	}
	fail := func(err error) int { fmt.Fprintln(stderr, "mekugi inspect-sessions:", err); return 1 }
	if *capture != "" && !inspector.HasProviderUsage() {
		return fail(errors.New("transport diagnostics are unavailable"))
	}

	if *directory == "" {
		codexDirectory := os.Getenv("CODEX_HOME")
		if codexDirectory == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return fail(err)
			}
			codexDirectory = filepath.Join(home, ".codex")
		}
		*directory = filepath.Join(codexDirectory, "sessions")
	}
	if *replay == "" {
		var err error
		*replay, err = history.ReplayDirectory()
		if err != nil {
			return fail(err)
		}
	}
	result, err := inspector.Corpus(ctx, query.CorpusOptions{Directory: *directory, ReplayDirectory: *replay, Since: since, Until: until,
		Model: *model, ExcludeModel: *exclude, Class: *class, Limit: *limit, Capture: *capture})
	if err != nil {
		return fail(err)
	}
	if err := json.MarshalEncode(jsontext.NewEncoder(stdout), &result, jsonv1.DefaultOptionsV1(), jsontext.EscapeForHTML(false)); err != nil {
		return fail(err)
	}
	return 0
}

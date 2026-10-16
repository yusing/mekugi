package router

import (
	"context"
	"crypto/sha256"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"unicode/utf8"

	"github.com/yusing/mekugi/libraries/legacycall"
	"github.com/yusing/mekugi/modules/commandsegments/reports"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

type storageInspection struct {
	Schema       string `json:"schema"`
	Target       string `json:"target"`
	Bytes        int    `json:"bytes"`
	SHA256       string `json:"sha256"`
	Dependencies int    `json:"dependencies"`
	Field        string `json:"field,omitempty"`
	Offset       int    `json:"offset,omitzero"`
	NextOffset   *int   `json:"next_offset,omitempty"`
	Text         string `json:"text,omitempty"`
}

// RunStorageInspection reads only an explicitly selected retained call or
// object. It does not open the writable store, acquire/create locks, scan other
// sessions, alter permissions or execute retained inputs.
func RunStorageInspection(service *history.Service, segments *reports.Service, ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("inspect-storage", flag.ContinueOnError)
	flags.SetOutput(stderr)
	replayDir := flags.String("replay-dir", "", "replay directory (default platform state directory)")
	workspace := flags.String("workspace", "", "absolute retained workspace identity (required with --call-id)")
	callID := flags.String("call-id", "", "one retained call identity")
	object := flags.String("object", "", "one snapshot object name, not a filesystem path")
	field := flags.String("field", "", "include text: all, baseline, exec, patches, review, or refs")
	offset := flags.Int("offset", 0, "UTF-8 byte offset into selected text")
	textBytes := flags.Int("text-bytes", 4096, "maximum UTF-8 bytes of selected text (4-65536)")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "Usage: mekugi inspect-storage --workspace DIR --call-id ID [options]")
		fmt.Fprintln(stderr, "       mekugi inspect-storage --object snapshot-HASH.json.gz [options]")
		fmt.Fprintln(stderr, "Validate selected persisted evidence without executing it. Private text is omitted unless --field is selected.")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 || (*callID == "") == (*object == "") ||
		(*callID != "" && !filepath.IsAbs(*workspace)) || (*object != "" && *workspace != "") ||
		*offset < 0 || *textBytes < 4 || *textBytes > 65536 ||
		(*field == "" && *offset != 0) ||
		(*field != "" && !slices.Contains([]string{"all", "baseline", "exec", "patches", "review", "refs"}, *field)) ||
		(*object != "" && *field != "" && *field != "all") {
		flags.Usage()
		return 2
	}
	fail := func(err error) int {
		fmt.Fprintln(stderr, "mekugi inspect-storage:", err)
		return 1
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if *replayDir == "" {
		var err error
		*replayDir, err = ReplayDirectory()
		if err != nil {
			return fail(err)
		}
	}
	selectedDirectory, err := filepath.Abs(*replayDir)
	if err != nil {
		return fail(err)
	}
	*replayDir = selectedDirectory
	store := &mekugiReplayStore{publication: service.Attach(*replayDir), directory: *replayDir}
	result := storageInspection{Schema: "mekugi.storage.v1", Field: *field}
	var data []byte
	if *object != "" {
		result.Target = *object
		var found bool
		var err error
		data, found, err = legacycall.SnapshotData(store.directory, *object, maxReplayRecordBytes)
		if err != nil {
			return fail(fmt.Errorf("object %s: %w", *object, err))
		}
		if !found {
			return fail(fmt.Errorf("object %s is unavailable", *object))
		}
		if !jsontext.Value(data).IsValid() {
			return fail(fmt.Errorf("object %s contains invalid JSON", *object))
		}
	} else {
		result.Target = legacycall.Name(*workspace, *callID, false)
		var r replayRecord
		var found bool
		var err error
		var dependencies []string
		if reports.IsID(*callID) {
			selected, exists, readErr := segments.Inspect(store.directory, *workspace, *callID)
			found, err = exists, readErr
			if exists {
				result.Target, dependencies, data = selected.Name, selected.Dependencies, selected.Data
				if old := selected.Legacy; old != nil {
					r = replayRecord{Version: old.Version, Workspace: old.Workspace, CallID: old.CallID, Commentary: old.Commentary, CaptureOrder: old.CaptureOrder, History: legacyHistory(old.History)}
				}
			}
		} else {
			r, found, err = store.read(*workspace, *callID, false)
			if err == nil && found {
				dependencies, err = store.publication.Dependencies(result.Target)
			}
		}
		if err != nil {
			return fail(fmt.Errorf("call %s: %w", *callID, err))
		}
		if !found {
			return fail(fmt.Errorf("call %s in workspace %s is unavailable", *callID, *workspace))
		}
		result.Dependencies = len(dependencies)
		value := any(r)
		if data != nil {
			value = jsontext.Value(data)
		}
		switch *field {
		case "baseline":
			value = r.History.ResolvedBaseline
		case "exec":
			value = r.History.ExecObservation
		case "patches":
			value = r.History.NativePatches
		case "review":
			value = r.History.ReviewFiles
		case "refs":
			value = dependencies
		}
		data, err = json.Marshal(value, json.Deterministic(true))
		if err != nil {
			return fail(err)
		}
	}
	result.Bytes, result.SHA256 = len(data), fmt.Sprintf("%x", sha256.Sum256(data))
	if *field != "" {
		if *offset > len(data) || *offset < len(data) && !utf8.RuneStart(data[*offset]) {
			return fail(fmt.Errorf("%s: offset must be a UTF-8 boundary within the selected text", result.Target))
		}
		end := min(len(data), *offset+*textBytes)
		for end < len(data) && !utf8.RuneStart(data[end]) {
			end--
		}
		result.Offset, result.Text = *offset, string(data[*offset:end])
		if end < len(data) {
			result.NextOffset = new(end)
		}
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if err := json.MarshalEncode(jsontext.NewEncoder(stdout), result); err != nil {
		return fail(err)
	}
	return 0
}

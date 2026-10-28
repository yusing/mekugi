package sessioncontrol

import (
	"context"
	"encoding/hex"
	json "encoding/json/v2"
	"errors"
	"path/filepath"
	"slices"
	"strings"

	"github.com/yusing/mekugi/contracts/recordkind"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

// ClaudeRecordKinds supplies native receipt codecs to the composed history
// service. It adds no independent store or logical coordination state.
func ClaudeRecordKinds() []recordkind.Spec {
	return []recordkind.Spec{{ID: "threadcontrol-runtime-claude", Match: func(name string) bool { return claudeRecordKind(name) != "" }, Dependencies: func(record recordkind.Record) ([]string, error) {
		var thread, session, id string
		var version int
		var dependencies []string
		kind := claudeRecordKind(record.Name)
		switch kind {
		case "session":
			var ledger claudeLedger
			if err := json.Unmarshal(record.Data, &ledger, json.RejectUnknownMembers(true)); err != nil {
				return nil, err
			}
			thread, session, id, version = ledger.Info.Thread, ledger.Info.Session, ledger.Info.Thread, ledger.Version
			for _, name := range ledger.Pending {
				if claudeRecordKind(name) != "input" || strings.Split(name, "-")[3] != strings.Split(record.Name, "-")[3] {
					return nil, errors.New("Claude ledger contains a foreign operation dependency")
				}
				dependencies = append(dependencies, name)
			}
		case "input":
			var receipt claudeReceipt
			if err := json.Unmarshal(record.Data, &receipt, json.RejectUnknownMembers(true)); err != nil {
				return nil, err
			}
			thread, session, id, version = receipt.Thread, receipt.Session, receipt.ID, receipt.Version
		case "terminal":
			var receipt claudeTerminal
			if err := json.Unmarshal(record.Data, &receipt, json.RejectUnknownMembers(true)); err != nil {
				return nil, err
			}
			thread, session, id, version = receipt.Thread, receipt.Session, receipt.Turn, receipt.Version
		case "interrupt":
			var receipt claudeInterrupt
			if err := json.Unmarshal(record.Data, &receipt, json.RejectUnknownMembers(true)); err != nil {
				return nil, err
			}
			thread, session, id, version = receipt.Thread, receipt.Session, receipt.Turn, receipt.Version
		}
		if version != 1 || thread == "" || session == "" || id == "" || record.Name != claudeRecordName(thread, kind, id) {
			return nil, errors.New("invalid Claude runtime receipt identity")
		}
		slices.Sort(dependencies)
		return dependencies, nil
	}, MaxRecordBytes: history.MaxRecordBytes}}
}

func claudeRecordKind(name string) string {
	if !strings.HasPrefix(name, "threadcontrol-runtime-claude-") || !strings.HasSuffix(name, ".json") {
		return ""
	}
	parts := strings.Split(strings.TrimSuffix(strings.TrimPrefix(name, "threadcontrol-runtime-claude-"), ".json"), "-")
	if len(parts) != 3 || parts[1] != "session" && parts[1] != "input" && parts[1] != "terminal" && parts[1] != "interrupt" {
		return ""
	}
	for _, hash := range []string{parts[0], parts[2]} {
		if len(hash) != 64 || strings.ToLower(hash) != hash {
			return ""
		}
		if _, err := hex.DecodeString(hash); err != nil {
			return ""
		}
	}
	return parts[1]
}

func readClaudeLedger(ctx context.Context, thread, path string) (claudeLedger, error) {
	var ledger claudeLedger
	if err := ctx.Err(); err != nil {
		return ledger, err
	}
	if !filepath.IsAbs(path) || filepath.Base(path) != claudeRecordName(thread, "session", thread) {
		return ledger, errors.New("Claude ledger path does not identify the selected thread")
	}
	if err := readClaudeRecord(filepath.Dir(path), filepath.Base(path), &ledger); err != nil {
		return ledger, err
	}
	if ledger.Version != 1 || ledger.Info.Thread != thread || ledger.Info.Session == "" || ledger.Info.Path != path || ledger.Info.Provider != "claude" {
		return ledger, errors.New("Claude ledger identity mismatch")
	}
	return ledger, nil
}

// ConfirmClaudeInput reads exact retained acceptance or terminal evidence without
// starting a runtime. Missing native acceptance remains uncertain, never retried.
func ConfirmClaudeInput(ctx context.Context, path string, input control.NativeInput) error {
	ledger, err := readClaudeLedger(ctx, input.Thread, path)
	if err != nil {
		return err
	}
	return confirmClaudeReceipt(ledger, input)
}

func ReadClaudeDetails(ctx context.Context, thread, path string) (control.SessionDetails, error) {
	var details control.SessionDetails
	ledger, err := readClaudeLedger(ctx, thread, path)
	if err == nil && ledger.Model != "" {
		details.Model = new(ledger.Model)
		details.SessionMetrics = ledger.Metrics.inspection()
	}
	return details, err
}

// ReadClaudeThread preserves retained native identity and fork lineage before
// launch selects the exact SDK session. It acquires no runtime or inference.
func ReadClaudeThread(ctx context.Context, thread, path string) (control.ThreadInfo, error) {
	ledger, err := readClaudeLedger(ctx, thread, path)
	return ledger.Info, err
}

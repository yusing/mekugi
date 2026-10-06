package router

import (
	"encoding/base64"
	"regexp"
	"strconv"
	"strings"

	"github.com/yusing/mekugi/internal/execsegment"
)

// Match only the generated shell grammar, not commands mentioning mjournal.
var nativeJournalCommand = regexp.MustCompile(`^mjournal --journal-once (\S+) ([A-Za-z0-9_-]+\.[A-Za-z0-9_-]+) '[A-Za-z0-9_.!~*()%+-]*'(?: [0-9]+ [a-f0-9]{64})?$`)

func (u *appServerUI) internalJournalCommand(thread string, item appServerItem) bool {
	if item.Type != "commandExecution" || u.proxy == nil || u.proxy.replayStore == nil {
		return false
	}
	parts := nativeJournalCommand.FindStringSubmatch(execsegment.ShOriginal(appServerDisplayCommand(item.Command)))
	if parts == nil {
		return false
	}
	encoded, _, _ := strings.Cut(parts[2], ".")
	callID, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return false
	}
	// Missing-metadata records keep their original empty workspace scope. Do
	// not rebase them; verify the retained executing thread in either scope.
	for _, workspace := range []string{u.session.cwd, ""} {
		history, found, err := u.proxy.replayStore.lookup(u.ctx, workspace, string(callID))
		if err == nil && found && history.ExecutingThread == thread && history.lowersJournalCommand(parts) {
			return true
		}
	}
	return false
}

// lowersJournalCommand reports whether parts, a nativeJournalCommand match,
// is a publication this translated exec call issues. The opaque token
// alone is not provenance: the exact generated prefix must belong to the
// durable carrier, including on resume.
func (h *mekugiHistory) lowersJournalCommand(parts []string) bool {
	if parts == nil || h.Script == h.CarrierPayload {
		return false
	}
	prefix := workerCommand("mjournal", []string{commentaryOnceArgument, parts[1], parts[2]})
	return strings.Contains(h.CarrierPayload, "const command = "+strconv.Quote(prefix+" '")+" + encodeURIComponent(JSON.stringify(mutation))")
}

package router

import (
	"encoding/base64"
	"encoding/json/v2"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/yusing/mekugi/internal/execsegment"
)

// Match only the generated shell grammar, not commands mentioning mjournal.
var nativeJournalCommand = regexp.MustCompile(`^mjournal --journal-once (\S+) ([A-Za-z0-9_-]+\.[A-Za-z0-9_-]+) '([A-Za-z0-9_.!~*()%+-]*)'(?: [0-9]+ [a-f0-9]{64})?$`)

// journalTransport presents a command item. A journal transport verified
// for thread is generated, so it never shows as the command it runs. A
// mutation is hidden: its persisted events have their own rows. A read or
// list changes nothing else visible, so it keeps a typed Read or List row
// naming its journal target, also the text of its tracked segment. ok is
// false for a hidden command; other items are shown unchanged.
func (u *appServerUI) journalTransport(thread string, item appServerItem) (shown appServerItem, operation string, ok bool) {
	parts := u.verifiedJournalCommand(thread, item)
	if parts == nil {
		return item, "", true
	}
	return journalTransportItem(item, parts)
}

func (u *appServerUI) verifiedJournalCommand(thread string, item appServerItem) []string {
	if item.Type != "commandExecution" || u.proxy == nil || u.proxy.replayStore == nil {
		return nil
	}
	parts := nativeJournalCommand.FindStringSubmatch(execsegment.ShOriginal(appServerDisplayCommand(item.Command)))
	if parts == nil {
		return nil
	}
	encoded, _, _ := strings.Cut(parts[2], ".")
	callID, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil
	}
	// Missing-metadata records keep their original empty workspace scope. Do
	// not rebase them; verify the retained executing thread in either scope.
	for _, workspace := range []string{u.session.cwd, ""} {
		history, found, err := u.proxy.replayStore.lookup(u.ctx, workspace, string(callID))
		if err == nil && found && history.ExecutingThread == thread && history.lowersJournalCommand(parts) {
			return parts
		}
	}
	return nil
}

// journalTransportItem presents a verified transport from its match parts.
// The typed action replaces only the displayed classification; the
// retained host item keeps its command and output.
func journalTransportItem(item appServerItem, parts []string) (appServerItem, string, bool) {
	action, ok := journalReadAction(parts[3])
	if !ok {
		return item, "", false
	}
	item.CommandActions = []appServerCommandAction{action}
	return item, appServerCommandText(item, ""), true
}

// journalReadAction is the typed operation of a journal read or list
// payload, as the generated helper encodes it. Mutations, including batched
// arrays, have none.
func journalReadAction(payload string) (appServerCommandAction, bool) {
	decoded, err := url.PathUnescape(payload)
	if err != nil {
		return appServerCommandAction{}, false
	}
	var request struct {
		Op    string `json:"op"`
		P     string `json:"p"`
		Agent string `json:"agent"`
	}
	if json.Unmarshal([]byte(decoded), &request) != nil {
		return appServerCommandAction{}, false
	}
	target := strings.Join(slices.DeleteFunc([]string{"journal", request.Agent, request.P}, func(part string) bool { return part == "" }), " ")
	switch request.Op {
	case "read":
		return appServerCommandAction{Type: "read", Path: target}, true
	case "list":
		return appServerCommandAction{Type: "listFiles", Path: target}, true
	}
	return appServerCommandAction{}, false
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

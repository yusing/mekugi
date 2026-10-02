package router

import (
	"context"
	jsonv1 "encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
)

// Root visibility is a separate audience from the child's stream and completion
// result. Reuse durable ancestry and event windows, not a transient activity queue.
// Publication needs a writable root response; it never wakes the root model.
func (t *mekugiResponseTransform) prepareRootJournalLive() []map[string]jsonv1.RawMessage {
	if t.journalLiveBytes >= maxCommentaryPublicationBytes {
		return nil
	}
	if store := t.proxy.replayStore; store != nil && t.journalRootQuietFile != nil {
		info, err := os.Lstat(store.directory)
		if err == nil && os.SameFile(info, t.journalRootQuietFile) && info.ModTime() == t.journalRootQuietFile.ModTime() {
			return nil
		}
	}
	t.journalRootQuietFile = nil
	ownedLease := t.journalDeliveryRelease == nil
	if ownedLease {
		release, err := t.proxy.journals.lockDelivery(t.ctx, t.proxy.replayStore, t.directory)
		if err != nil {
			return nil
		}
		t.journalDeliveryRelease = release
	}
	var messages []map[string]jsonv1.RawMessage
	defer func() {
		if ownedLease && len(messages) == 0 {
			t.ReleaseDelivery()
		}
	}()
	release, err := t.proxy.journals.lockState(t.ctx)
	if err != nil {
		return nil
	}
	var children []threadJournal
	read := func() error {
		// Remember the directory before reading. Atomic journal replacement
		// changes it, including a child mutation after this snapshot.
		if t.proxy.replayStore != nil {
			t.journalRootQuietFile, _ = os.Lstat(t.proxy.replayStore.directory)
		}
		var err error
		children, err = t.proxy.journals.descendants(t.proxy.replayStore, t.directory, t.shellThreadID)
		return err
	}
	if t.proxy.replayStore != nil {
		err = t.proxy.replayStore.locked(t.ctx, read)
	} else {
		err = read()
	}
	release()
	if err != nil {
		t.journalRootQuietFile = nil
		t.proxy.notice(t.sessionID, t.shellThreadID, "journal_child_live", "Mekugi could not read child journal live updates: "+err.Error()+". Child execution and results are unchanged.")
		return nil
	}
	for _, child := range children {
		text, sequence := rootJournalLiveText(child, maxCommentaryPublicationBytes-t.journalLiveBytes)
		if text == "" {
			continue
		}
		// Retry remaining windows after this one is confirmed, even if this
		// process has no durable store whose replacement changes the directory.
		t.journalRootQuietFile = nil
		id := commentaryMessageID(fmt.Sprintf("journal-root-live\x00%s\x00%s\x00%s\x00%d", t.directory, t.shellThreadID, child.Thread, sequence))
		message := assistantCommentaryMessage(id, text)
		if len(t.retainCommentary(message)) == 0 {
			continue
		}
		if t.journalDeliveries == nil {
			t.journalDeliveries = make(map[string]journalDelivery)
		}
		t.journalDeliveries[id] = journalDelivery{thread: child.Thread, sequence: sequence, rootLive: true}
		t.journalLiveBytes += len(text)
		messages = append(messages, message)
	}
	return messages
}

func rootJournalLiveText(j threadJournal, budget int) (string, uint64) {
	header := "Journal\n\n" + commentaryCode(j.Author)
	const clipped = "\n  … (clipped; read the child journal for full details)"
	limit := maxCommentaryPublicationBytes - len(header)
	if limit <= len(clipped) || len(header) >= budget {
		return "", 0
	}
	var output strings.Builder
	output.WriteString(header)
	sequence := uint64(0)
	for _, event := range j.Events {
		if event.Seq <= j.RootLiveSeq || event.Fields.Kind == "answer" {
			continue
		}
		if event.Legacy {
			// Retained v1 authoring keeps report_now opt-in. Child delivery
			// may already have acknowledged the item, so ignore Reported.
			index := slices.IndexFunc(j.Items, func(item journalItem) bool { return item.Path == event.Path })
			if event.Op == "remove" {
				if !event.RootRetraction {
					continue
				}
			} else if index < 0 || !j.Items[index].ReportNow || j.Items[index].TerminalOnly || j.Items[index].Updated != event.Seq {
				continue
			}
		}
		row := "\n- " + strings.TrimPrefix(indentJournalText(journalEventText(event), "  "), "  ")
		if len(row) > limit {
			row = strings.ToValidUTF8(row[:limit-len(clipped)], "") + clipped
		}
		if output.Len()+len(row) > budget {
			break
		}
		output.WriteString(row)
		sequence = event.Seq
	}
	if sequence == 0 {
		return "", 0
	}
	return output.String(), sequence
}

func (s *journalStore) acknowledgeRootLive(ctx context.Context, store *mekugiReplayStore, workspace, thread string, sequence uint64) error {
	return s.transaction(ctx, store, workspace, thread, func(j *threadJournal, exists bool) error {
		if !exists {
			return errors.New("child journal live cursor state is missing")
		}
		// The delivery lease keeps the item snapshot stable until confirmation.
		// Silent revisions must not erase that an earlier revision was shown.
		for i := range j.Items {
			item := &j.Items[i]
			if item.Updated > j.RootLiveSeq && item.Updated <= sequence && item.ReportNow && !item.TerminalOnly {
				item.RootEverReported = true
			}
		}
		j.RootLiveSeq = max(j.RootLiveSeq, sequence)
		return nil
	})
}

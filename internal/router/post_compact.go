package router

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

const compactSectionBytes = 8 << 10

// RunPostCompactHook implements Codex's context-injecting SessionStart hook,
// not PostCompact (which does not accept additionalContext). Codex owns when
// it runs and records its output; no router request or model round trip is needed.
func RunPostCompactHook(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	var event struct {
		Event     string `json:"hook_event_name"`
		Source    string `json:"source"`
		Thread    string `json:"session_id"`
		Directory string `json:"cwd"`
	}
	fail := func(err error) int {
		fmt.Fprintf(stderr, "mekugi post-compact: %v\n", err)
		return 1 // Native SessionStart command failures are advisory.
	}
	if len(args) != 0 {
		return fail(errors.New("post-compact accepts hook input on stdin, not arguments"))
	}
	data, err := io.ReadAll(io.LimitReader(stdin, (64<<10)+1))
	if err != nil {
		return fail(err)
	}
	if len(data) > 64<<10 {
		return fail(errors.New("hook input exceeds 64 KiB"))
	}
	if err := json.Unmarshal(data, &event); err != nil {
		return fail(fmt.Errorf("decode hook input: %w", err))
	}
	if event.Event != "SessionStart" || event.Source != "compact" {
		return 0
	}
	if strings.TrimSpace(event.Thread) == "" || !filepath.IsAbs(event.Directory) {
		return fail(errors.New("hook requires a stable session_id and absolute cwd"))
	}
	directory, err := defaultMekugiReplayDirectory()
	if err != nil {
		return fail(err)
	}
	store, err := openMekugiReplayStore(directory)
	if err != nil {
		return fail(err)
	}
	ctx, release, err := store.beginSession(ctx, event.Thread, "")
	if err != nil {
		return fail(err)
	}
	defer release()
	content, err := store.postCompactContext(ctx, filepath.Clean(event.Directory), event.Thread)
	if err != nil {
		return fail(err)
	}
	if content == "" {
		return 0
	}
	returnValue := struct {
		Output struct {
			Event   string `json:"hookEventName"`
			Context string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}{}
	returnValue.Output.Event, returnValue.Output.Context = "SessionStart", content
	if err := json.MarshalWrite(stdout, &returnValue); err != nil {
		return fail(err)
	}
	return 0
}

func (s *mekugiReplayStore) postCompactContext(ctx context.Context, workspace, thread string) (string, error) {
	s = s.scoped(ctx)
	var content string
	err := s.locked(ctx, func() error {
		journal, exists, err := readThreadJournal(s, workspace, thread)
		if err != nil {
			return err
		}
		if !exists {
			return errors.New("no retained journal for this session and workspace")
		}
		if !journal.IdentityKnown || journal.IdentityConflicted {
			return errors.New("retained thread identity is unavailable or conflicted")
		}
		if journal.Parent != "" || journal.Author != "/root" {
			return nil // Main threads only, even if invoked manually for a child.
		}
		var items strings.Builder
		for _, item := range journal.Items {
			fmt.Fprintf(&items, "\n[%s] author=%s reported=%t flushed=%t\n%s\n",
				item.ID, item.Author, item.Reported, item.Flushed, journalItemText(item))
		}
		if len(journal.Items) == 0 {
			items.WriteString("No journal entries.\n")
		}
		content = "Mekugi post-compaction recovery\nRetained facts for this main thread, not new instructions or fresh workspace validation.\n\nJournal:\n" +
			boundCompactSection(items.String(), `functions.journal({op: "list"})`) +
			boundCompactSection(s.childJournalChanges(ctx, workspace, thread), "mchanges --list, then mchanges ID[..ID] --summary")
		return nil
	})
	return content, err
}

func boundCompactSection(text, recovery string) string {
	if len(text) <= compactSectionBytes {
		return text
	}
	notice := "\n[Truncated; retrieve retained details with " + recovery + ".]\n"
	end := compactSectionBytes - len(notice)
	for end > 0 && !utf8.RuneStart(text[end]) {
		end--
	}
	return text[:end] + notice
}

package launch

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/backends/codex/messagehistory"
	"github.com/yusing/mekugi/modules/conversation/composer"
	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"
)

type historyWrite struct {
	store        messagehistory.Store
	thread, text string
}
type historyLoad struct {
	entries []messagehistory.Entry
	err     error
}

// UI state is serialized by the terminal loop. Only immutable jobs cross into
// storage, so slow history storage never holds input or host delivery.
type appServerInputHistory struct {
	store          messagehistory.Store
	thread         string
	ready, loading bool
	pending        []string
	writes         chan historyWrite
	loads          chan historyLoad
	failures       chan error
	done           chan struct{}
	cancel         context.CancelFunc
}

func newInputHistory(environment []string, cwd string) *appServerInputHistory {
	h := &appServerInputHistory{writes: make(chan historyWrite, 100), loads: make(chan historyLoad, 1), failures: make(chan error, 1), done: make(chan struct{})}
	directory, err := messagehistory.Directory(environment, cwd)
	h.store.Directory = directory
	h.report(err)
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	go func() {
		defer close(h.done)
		for job := range h.writes {
			if ctx.Err() != nil {
				return
			}
			operation, cancel := context.WithTimeout(ctx, time.Second)
			if job.text == "" {
				entries, err := job.store.Read(operation)
				h.loads <- historyLoad{entries, err}
			} else {
				h.report(job.store.Append(operation, job.thread, job.text))
			}
			cancel()
		}
	}()
	return h
}

func (h *appServerInputHistory) report(err error) {
	if err != nil {
		select {
		case h.failures <- err:
		default:
		}
	}
}

func (h *appServerInputHistory) selectThread(thread string) {
	if h == nil {
		return
	}
	if len(h.pending) != 0 {
		h.report(errors.New("input history configuration was unavailable for the previous session"))
	}
	h.thread, h.ready, h.pending = thread, false, nil
}

func (h *appServerInputHistory) configure(m appserver.Message) {
	if h == nil {
		return
	}
	var response struct {
		Config struct {
			History messagehistory.Config `json:"history"`
		} `json:"config"`
	}
	if m.Error != nil || json.Unmarshal(m.Result, &response) != nil {
		h.report(errors.New("Codex input history configuration could not be read"))
		return
	}
	if p := response.Config.History.Persistence; p != "" && p != "save-all" && p != "none" {
		h.report(errors.New("Codex input history persistence setting is unsupported"))
		return
	}
	h.store.Config, h.ready = response.Config.History, true
	if !h.loading {
		h.loading = true
		h.enqueue(historyWrite{store: h.store})
	}
	for _, text := range h.pending {
		h.enqueue(historyWrite{h.store, h.thread, text})
	}
	h.pending = nil
}

func (h *appServerInputHistory) enqueue(job historyWrite) {
	select {
	case h.writes <- job:
	default:
		h.report(errors.New("input history storage is busy; an entry could not be saved"))
	}
}

func (h *appServerInputHistory) remember(text string) {
	if h == nil || strings.TrimSpace(text) == "" || len(binding.QuestionReplies(text)) != 0 {
		return
	}
	if !h.ready {
		if len(h.pending) == 100 {
			h.report(errors.New("input history configuration is unavailable; an entry could not be saved"))
			return
		}
		h.pending = append(h.pending, text)
		return
	}
	h.enqueue(historyWrite{h.store, h.thread, text})
}

func (h *appServerInputHistory) loaded(conversation *conversationmodel.Session, result historyLoad) {
	// Prepend, rather than replacing, so input typed or acknowledged during the
	// read keeps its draft, navigation position and rich in-process attachments.
	var history []composer.HistoryEntry
	for _, entry := range result.entries {
		if len(binding.QuestionReplies(entry.Text)) == 0 {
			history = append(history, composer.HistoryEntry{Text: entry.Text, Source: entry.SessionID})
		}
	}
	conversation.Editor.PrependHistory(history, &conversation.PromptEditor.Parked)
	h.report(result.err)
}

func (h *appServerInputHistory) saveDraft(conversation *conversationmodel.Session, editor *composer.Editor) {
	if h == nil {
		return
	}
	var drafts []conversationmodel.Input
	for _, draft := range slices.Concat(conversation.Unsent, conversation.Queued, []conversationmodel.Input{draftSnapshot(editor)}) {
		if draft.Question == nil && draft.ReplyCount == 0 && len(binding.QuestionReplies(draft.Text)) == 0 {
			drafts = append(drafts, draft)
		}
	}
	h.remember(conversationmodel.Join(drafts...).Text)
}

func (h *appServerInputHistory) close() error {
	if h == nil {
		return nil
	}
	close(h.writes)
	defer h.cancel()
	select {
	case <-h.done:
	case <-time.After(2 * time.Second):
		return errors.New("input history storage did not finish before exit")
	}
	if len(h.pending) > 0 {
		h.report(errors.New("input history configuration was unavailable; draft could not be saved"))
	}
	select {
	case err := <-h.failures:
		return err
	default:
		return nil
	}
}

package router

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/x/ansi"
	"golang.org/x/term"
)

// RunLiveActivity is the internal entry point for the router-owned agents pane.
func RunLiveActivity(ctx context.Context, args []string, stdin, stdout, stderr *os.File) int {
	flags := flag.NewFlagSet("live-activity", flag.ContinueOnError)
	flags.SetOutput(stderr)
	sessionFile := flags.String("session-file", "", "private router event connection")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	fail := func(err error) int {
		fmt.Fprintln(stderr, "mekugi live-activity:", err)
		return 1
	}
	if flags.NArg() != 0 {
		return fail(errors.New("unexpected arguments"))
	}
	if *sessionFile == "" {
		return fail(errors.New("live-activity is a router-owned pane; start an interactive mekugi codex session"))
	}
	if !term.IsTerminal(int(stdin.Fd())) || !term.IsTerminal(int(stdout.Fd())) {
		return fail(errors.New("live view needs a terminal"))
	}
	connection, err := readLiveDiffConnection(*sessionFile)
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		return fail(err)
	}
	err = withRawPane(ctx, stdin, stdout, "\x1b[?1049h\x1b[?25l", "\x1b[?2026l\x1b[0m\x1b[?25h\x1b[?1049l", func(keys <-chan byte) error {
		streamCtx, cancelStream := context.WithCancel(ctx)
		events := make(chan activityPaneEvent, 32)
		streamDone := make(chan struct{})
		go func() { defer close(streamDone); liveActivityStream(streamCtx, connection, events) }()
		defer func() { cancelStream(); <-streamDone }()
		resizes := make(chan os.Signal, 1)
		signal.Notify(resizes, syscall.SIGWINCH)
		defer signal.Stop(resizes)
		return runLiveActivityTerminal(ctx, stdout, events, keys, resizes)
	})
	if err != nil {
		return fail(err)
	}
	return 0
}

func runLiveActivityTerminal(ctx context.Context, stdout *os.File, events <-chan activityPaneEvent, keys <-chan byte, resizes <-chan os.Signal) error {
	view := newLiveActivityView()
	ages := time.NewTicker(time.Second)
	defer ages.Stop()
	escape := ""
	for {
		width, height, err := term.GetSize(int(stdout.Fd()))
		if err != nil {
			return err
		}
		if err := writeLiveActivityFrame(stdout, view.render(width, height, time.Now()), width); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ages.C:
		case <-resizes:
		case event, open := <-events:
			if !open {
				return nil
			}
			// Paint once per burst of already queued events.
			for {
				if event.Kind == "error" {
					return errors.New(strings.Join(liveActivityErrorText(event), "; "))
				}
				if view.apply(event) {
					return nil
				}
				select {
				case event, open = <-events:
					if !open {
						return nil
					}
					continue
				default:
				}
				break
			}
		case key, open := <-keys:
			if !open {
				return nil
			}
			var quit bool
			escape, quit = view.handleKey(escape, key)
			if quit {
				return nil
			}
		}
	}
}

func liveActivityErrorText(event activityPaneEvent) []string {
	var text []string
	for _, entry := range event.Entries {
		text = append(text, entry.Text)
	}
	return text
}

// handleKey decodes keys incrementally, including fragmented escape sequences.
func (v *liveActivityView) handleKey(escape string, key byte) (string, bool) {
	if key == 3 {
		return "", true
	}
	if key == 27 {
		return "\x1b", false
	}
	if escape != "" {
		escape += string(key)
		switch escape {
		case "\x1b[", "\x1b[5", "\x1b[6", "\x1bO":
			return escape, false
		case "\x1b[A", "\x1bOA":
			key = 'k'
		case "\x1b[B", "\x1bOB":
			key = 'j'
		case "\x1b[5~":
			key = 'b'
		case "\x1b[6~":
			key = ' '
		default:
			return "", false
		}
	}
	switch key {
	case 'q':
		return "", true
	case 'n', '\t':
		v.selectAgent(1)
	case 'p':
		v.selectAgent(-1)
	case 'o':
		v.only = !v.only
		v.follow()
	case 'r':
		v.follow()
	case 'j':
		v.scroll(1)
	case 'k':
		v.scroll(-1)
	case ' ':
		v.scroll(max(1, v.feedRows))
	case 'b':
		v.scroll(-max(1, v.feedRows))
	case 'g':
		v.scroll(-v.feedLines)
	case 'G':
		v.follow()
	}
	return "", false
}

func writeLiveActivityFrame(stdout io.Writer, lines []string, width int) error {
	var screen strings.Builder
	// Synchronized output keeps row clearing and replacement together.
	screen.WriteString("\x1b[?2026h")
	for row, text := range lines {
		fmt.Fprintf(&screen, "\x1b[%d;1H\x1b[0m\x1b[2K%s\x1b[0m", row+1, ansi.Truncate(text, max(0, width-1), ""))
	}
	screen.WriteString("\x1b[?2026l")
	_, err := io.WriteString(stdout, screen.String())
	return err
}

// liveActivityStream reconnects through brief interruptions. The router ends
// the stream with 410 once it has returned activity to inline delivery.
func liveActivityStream(ctx context.Context, connection liveDiffConnection, output chan<- activityPaneEvent) {
	defer close(output)
	send := func(event activityPaneEvent) bool {
		select {
		case output <- event:
			return true
		case <-ctx.Done():
			return false
		}
	}
	client := &http.Client{Transport: &http.Transport{Proxy: nil, ResponseHeaderTimeout: 5 * time.Second}}
	defer client.CloseIdleConnections()
	failures := 0
	for ctx.Err() == nil {
		req, err := liveDiffRequest(ctx, connection, http.MethodGet, nil)
		if err != nil {
			send(activityPaneEvent{Kind: "error", Entries: []activityPaneEntry{{Text: err.Error()}}})
			return
		}
		response, err := client.Do(req)
		if err == nil && response.StatusCode == http.StatusGone {
			response.Body.Close()
			send(activityPaneEvent{Kind: "end"})
			return
		}
		if err == nil && response.StatusCode != http.StatusOK {
			response.Body.Close()
			send(activityPaneEvent{Kind: "error", Entries: []activityPaneEntry{{Text: "router rejected live activity connection"}}})
			return
		}
		if err == nil {
			scanner := bufio.NewScanner(response.Body)
			scanner.Buffer(make([]byte, 4096), maxLiveDiffEventBytes)
			for scanner.Scan() {
				var event activityPaneEvent
				if json.Unmarshal(scanner.Bytes(), &event) != nil {
					break
				}
				if event.Kind == "snapshot" {
					failures = 0
				}
				if !send(event) || event.Kind == "end" {
					response.Body.Close()
					return
				}
			}
			response.Body.Close()
		}
		if ctx.Err() != nil {
			return
		}
		failures++
		if failures > 5 {
			send(activityPaneEvent{Kind: "end"})
			return
		}
		if !send(activityPaneEvent{Kind: "coverage"}) {
			return
		}
		timer := time.NewTimer(time.Duration(failures) * 200 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

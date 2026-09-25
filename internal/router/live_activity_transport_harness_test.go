package router

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"golang.org/x/term"
)

func runLiveActivityTerminal(ctx context.Context, stdout *os.File, view *liveActivityView, events <-chan activityPaneEvent, keys <-chan byte, resizes <-chan os.Signal) error {
	ages := time.NewTicker(time.Second)
	defer ages.Stop()
	escape := ""
	mouse := liveDiffMouse{}
	redraw := true
	for {
		if redraw {
			width, height, err := term.GetSize(int(stdout.Fd()))
			if err != nil {
				return err
			}
			if err := writeLiveActivityFrame(stdout, view.render(width, height, time.Now()), width); err != nil {
				return err
			}
			redraw = false
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ages.C:
			redraw = true
		case <-resizes:
			redraw = true
		case event, open := <-events:
			redraw = true
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
			if key == 27 {
				mouse = liveDiffMouse{}
			}
			if mouse.active || escape == "\x1b[" && key == '<' {
				escape = ""
				action, row, column := mouse.consume(key)
				redraw = view.handleMouse(action, row, column)
			} else {
				escape, quit = view.handleKey(escape, key)
				redraw = escape == ""
			}
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

// liveActivityStream owns one viewer connection for the session lifetime.
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
	req, err := liveDiffRequest(ctx, connection, http.MethodGet, nil)
	if err != nil {
		send(activityPaneEvent{Kind: "error", Entries: []activityPaneEntry{{Text: err.Error()}}})
		return
	}
	response, err := client.Do(req)
	if err != nil {
		if ctx.Err() == nil {
			send(activityPaneEvent{Kind: "error", Entries: []activityPaneEntry{{Text: err.Error()}}})
		}
		return
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusGone {
		send(activityPaneEvent{Kind: "end"})
		return
	}
	if response.StatusCode != http.StatusOK {
		send(activityPaneEvent{Kind: "error", Entries: []activityPaneEntry{{Text: "router rejected live activity connection"}}})
		return
	}
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 4096), maxLiveDiffEventBytes)
	for scanner.Scan() {
		var event activityPaneEvent
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			send(activityPaneEvent{Kind: "error", Entries: []activityPaneEntry{{Text: "invalid live activity event"}}})
			return
		}
		if !send(event) || event.Kind == "end" {
			return
		}
	}
	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		send(activityPaneEvent{Kind: "error", Entries: []activityPaneEntry{{Text: err.Error()}}})
		return
	}
	if ctx.Err() == nil {
		send(activityPaneEvent{Kind: "end"})
	}
}

// liveActivityOSCByte reports whether key can occur in an OSC 11 reply such
// as "11;rgb:ffff/ffff/ffff" with a BEL or ST terminator.

package router

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/yusing/mekugi"
	"golang.org/x/term"
)

// The simulation uses the production projection worker, authenticated event
// transport, durable captures, and terminal viewer. All source and evidence
// belong to one disposable directory, never the caller's workspace.
func runLiveDiffSimulation(ctx context.Context, stdin, stdout *os.File, speed float64, repeat bool) error {
	if math.IsNaN(speed) || speed < .1 || speed > 20 {
		return errors.New("simulation speed must be between 0.1 and 20")
	}
	if !term.IsTerminal(int(stdin.Fd())) || !term.IsTerminal(int(stdout.Fd())) {
		return errors.New("live diff simulation needs a terminal")
	}
	directory, err := os.MkdirTemp("", "mekugi-live-diff-simulation-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(directory)
	workspace := filepath.Join(directory, "workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		return err
	}
	store, err := openMekugiReplayStore(filepath.Join(directory, "replay"))
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	broker := newLiveDiffBroker(ctx)
	broker.setScope(liveDiffScope{Workspaces: map[string]map[string]bool{workspace: {"simulation": true}}})
	store.liveDiff = broker.publish
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+liveDiffEventsPath, broker.serveEvents)
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	broker.setEndpoint("http://" + listener.Addr().String() + liveDiffEventsPath)
	serverDone := make(chan struct{})
	go func() { defer close(serverDone); _ = server.Serve(listener) }()
	defer func() { cancel(); server.Close(); <-serverDone }()
	connection := filepath.Join(directory, "connection.json")
	if err := os.WriteFile(connection, mustMarshalJSON(broker.descriptor()), 0600); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() {
		err := playLiveDiffSimulation(ctx, broker, store, workspace, speed, repeat)
		if err != nil && ctx.Err() == nil {
			broker.mu.Lock()
			broker.emitLocked(liveDiffEvent{Kind: "coverage", Status: "SIMULATION: failed: " + err.Error()})
			broker.mu.Unlock()
		}
		done <- err
	}()
	err = runLiveDiffTerminal(ctx, store, workspace, stdin, stdout, connection)
	cancel()
	playErr := <-done
	if errors.Is(playErr, context.Canceled) {
		playErr = nil
	}
	return errors.Join(err, playErr)
}

func playLiveDiffSimulation(ctx context.Context, broker *liveDiffBroker, store *mekugiReplayStore, workspace string, speed float64, repeat bool) error {
	pause := func(duration time.Duration) error {
		timer := time.NewTimer(time.Duration(float64(duration) / speed))
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return nil
		}
	}
	status := func(text string) {
		broker.mu.Lock()
		broker.emitLocked(liveDiffEvent{Kind: "coverage", Status: "SIMULATION: " + text})
		broker.mu.Unlock()
	}
	sequence := 0
	// Files are controlled fixture names. Publish applied evidence only after
	// the actual isolated file write succeeds.
	apply := func(name, content string) error {
		path := filepath.Join(workspace, name)
		before, err := os.ReadFile(path)
		script := "new " + name + "\ntype " + strconv.Quote(content)
		if err == nil {
			script = "in " + name + "\ntype " + strconv.Quote(string(before)) + " " + strconv.Quote(content)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		files, err := mekugi.PreviewForHostAt(ctx, workspace, script)
		if err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			return err
		}
		sequence++
		call := fmt.Sprintf("simulation-%d", sequence)
		id, err := store.reserveChange(ctx, workspace, "simulation", call)
		if err != nil {
			return err
		}
		return store.put(ctx, workspace, map[string]mekugiHistory{call: {
			Script: script, ChangeID: id, CorrelationID: call, Applied: true, ReviewFiles: files,
		}})
	}
	if err := apply("streaming.txt", "anchor\n"); err != nil {
		return err
	}
	var captured strings.Builder
	for row := 1; row <= 120; row++ {
		fmt.Fprintf(&captured, "captured line %03d: scroll here while the independent preview streams\n", row)
	}
	if err := apply("captured.txt", captured.String()); err != nil {
		return err
	}
	for cycle := 1; ; cycle++ {
		if err := pause(2 * time.Second); err != nil {
			return err
		}
		status(fmt.Sprintf("cycle %d · j/k scroll diff · r follow · resize terminal · q quit", cycle))
		worker := startLiveDiffPreview(ctx, broker, workspace, "simulation")
		worker.appendDelta("in streaming.txt\ntype \"anchor\\n\" <<PATCH\n")
		var content strings.Builder
		for row := 1; row <= 100; row++ {
			line := fmt.Sprintf("streamed line %03d: watch the newest row stay in view\n", row)
			if row == 60 {
				line = "wrapped line: " + strings.Repeat("long source ", 25) + "TIP\n"
			}
			content.WriteString(line)
			// Two fragments per row exercise unfinished values, not frame fixtures.
			middle := len(line) / 2
			worker.appendDelta(line[:middle])
			if err := pause(45 * time.Millisecond); err != nil {
				worker.stop()
				<-worker.done
				return err
			}
			worker.appendDelta(line[middle:])
			if err := pause(45 * time.Millisecond); err != nil {
				worker.stop()
				<-worker.done
				return err
			}
		}
		status("burst · superseded preview frames are skipped")
		for row := 101; row <= 300; row++ {
			line := fmt.Sprintf("burst line %03d\n", row)
			content.WriteString(line)
			worker.appendDelta(line)
		}
		if err := pause(time.Second); err != nil {
			worker.stop()
			<-worker.done
			return err
		}
		worker.stop()
		<-worker.done
		if err := apply("streaming.txt", content.String()); err != nil {
			return err
		}
		status("completed · preview hides after 300 ms; diff regains its height")
		if err := pause(2 * time.Second); err != nil {
			return err
		}
		if err := apply("streaming.txt", "anchor\n"); err != nil {
			return err
		}
		status("interruption · this next preview will not be applied")
		worker = startLiveDiffPreview(ctx, broker, workspace, "simulation")
		worker.appendDelta("in streaming.txt\ntype \"anchor\\n\" \"interrupted preview")
		if err := pause(time.Second); err != nil {
			worker.stop()
			<-worker.done
			return err
		}
		worker.stop()
		<-worker.done
		status("finished · rerun the same command to replay, or use --repeat · q quit")
		if !repeat {
			return nil
		}
		if err := pause(2 * time.Second); err != nil {
			return err
		}
	}
}

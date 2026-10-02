package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/yusing/mekugi/internal/claude"
	"github.com/yusing/mekugi/internal/router"
	"golang.org/x/term"
)

func runClaude(ctx context.Context, args []string, in, out *os.File, stderr io.Writer) int {
	flags := flag.NewFlagSet("mekugi claude", flag.ContinueOnError)
	flags.SetOutput(stderr)
	cwd := flags.String("cwd", ".", "workspace directory")
	resume := flags.String("resume", "", "Claude session ID to resume")
	model := flags.String("model", "", "native Claude model choice")
	bridge := flags.String("bridge", "", "path to built Claude SDK bridge")
	flags.Usage = func() {
		fmt.Fprint(stderr, "Usage: mekugi claude [--cwd DIR] [--resume SESSION] [--model MODEL]\n\nPresentation-only Claude Code client. Requires Node and an installed, authenticated\nClaude Code runtime. Build this preview with make build-claude. No router or companion.\n")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "mekugi claude: unexpected positional arguments")
		return 2
	}
	fail := func(err error) int { fmt.Fprintln(stderr, "mekugi claude:", err); return 1 }
	if !term.IsTerminal(int(in.Fd())) || !term.IsTerminal(int(out.Fd())) {
		return fail(errors.New("interactive terminal required"))
	}
	workspace, err := filepath.Abs(*cwd)
	if err != nil {
		return fail(err)
	}
	info, err := os.Stat(workspace)
	if err != nil {
		return fail(err)
	}
	if !info.IsDir() {
		return fail(errors.New("workspace must be a directory"))
	}
	node, err := exec.LookPath("node")
	if err != nil {
		return fail(errors.New("Node.js is required"))
	}
	executable, err := exec.LookPath("claude")
	if err != nil {
		return fail(errors.New("install and sign in with the official Claude Code CLI first"))
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return fail(err)
	}
	if *bridge == "" {
		own, err := os.Executable()
		if err != nil {
			return fail(err)
		}
		*bridge = filepath.Join(filepath.Dir(own), "claude-bridge", "dist", "bridge.js")
	}
	*bridge, err = filepath.Abs(*bridge)
	if err != nil {
		return fail(err)
	}
	if _, err := os.Stat(*bridge); err != nil {
		return fail(fmt.Errorf("Claude bridge unavailable; run make build-claude: %w", err))
	}
	client, err := claude.Start(ctx, node, *bridge, claude.Config{Cwd: workspace, Executable: executable, Resume: *resume, Model: *model})
	if err != nil {
		return fail(err)
	}
	err = router.RunNativeSession(ctx, client, "Claude Code", workspace, in, out)
	closeErr := client.Close()
	if err != nil {
		return fail(err)
	}
	if closeErr != nil {
		return fail(closeErr)
	}
	return 0
}

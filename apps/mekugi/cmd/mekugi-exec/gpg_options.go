package main

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/yusing/mekugi/backends/codex/shellapproval"
)

// GPG publishes its own gpgrt option table: name:option-code:flags:description.
// The low three flag bits specify argument type; bit 3 marks an optional argument.
// Query the selected executable rather than retaining a second option catalog.
func gpgOptionTable(executable string) (map[string]int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "--no-options", "--dump-option-table")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(stdout, shellapproval.MaxMessage+1))
	if len(data) > shellapproval.MaxMessage {
		err = fmt.Errorf("GPG option table exceeds limit")
	}
	if err != nil {
		cmd.Process.Kill()
	}
	if wait := cmd.Wait(); wait != nil {
		return nil, wait
	}
	if err != nil {
		return nil, err
	}
	options := make(map[string]int)
	for line := range strings.SplitSeq(string(data), "\n") {
		fields := strings.SplitN(line, ":", 4)
		if len(fields) != 4 || fields[0] == "" {
			continue
		}
		flags, err := strconv.Atoi(fields[2])
		if err != nil {
			return nil, err
		}
		options["--"+fields[0]] = flags
		if short, err := strconv.Atoi(fields[1]); err == nil && short > 0 && short < 128 {
			options["-"+string(rune(short))] = flags
		}
	}
	if _, ok := options["--pinentry-mode"]; !ok {
		return nil, fmt.Errorf("missing GPG option table")
	}
	return options, nil
}

// Preserve native argv on ambiguous/unknown options. Operands, including those
// spelling authentication options, are never interpreted as another option.
func gpgArguments(args []string, options map[string]int) (forward []string, status int, interactive bool) {
	status = -1
	for i := 0; i < len(args); i++ {
		start := i
		arg := args[i]
		if arg == "--" || !strings.HasPrefix(arg, "-") || arg == "-" {
			return append(forward, args[i:]...), status, true
		}
		name, value, attached := strings.Cut(arg, "=")
		if strings.HasPrefix(arg, "--") {
			if _, ok := options[name]; !ok {
				match := ""
				for candidate := range options {
					if strings.HasPrefix(candidate, name) {
						if match != "" {
							return args, -1, false
						}
						match = candidate
					}
				}
				name = match
			}
			flags, known := options[name]
			if !known {
				return args, -1, false
			}
			switch name {
			case "--pinentry-mode", "--passphrase", "--passphrase-fd", "--passphrase-file", "--command-fd", "--command-file", "--status-file", "--options":
				return args, -1, false
			}
			if flags&7 != 0 && !attached && (flags&8 == 0 || i+1 < len(args) && !strings.HasPrefix(args[i+1], "-")) {
				i++
				if i == len(args) {
					return args, -1, false
				}
				value = args[i]
			}
			if name == "--status-fd" {
				var err error
				status, err = strconv.Atoi(value)
				if err != nil || status < 0 {
					return args, -1, false
				}
				continue
			}
		} else {
			for j := 1; j < len(arg); j++ {
				flags, known := options["-"+arg[j:j+1]]
				if !known {
					return args, -1, false
				}
				if flags&7 == 0 {
					continue
				}
				if j+1 == len(arg) && (flags&8 == 0 || i+1 < len(args) && !strings.HasPrefix(args[i+1], "-")) {
					i++
				}
				if i == len(args) {
					return args, -1, false
				}
				break
			}
		}
		forward = append(forward, args[start:i+1]...)
	}
	return forward, status, true
}

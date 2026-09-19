//go:build linux || darwin

package router

import (
	"context"
	"fmt"
	"strings"

	"golang.org/x/sys/unix"
	"mvdan.cc/sh/v3/interp"
)

// Resource limits belong to the worker process, not an interpreter subshell.
// Inspection is safe; mutation would also affect unrelated concurrent jobs.
func executeShellUlimit(ctx context.Context, args []string) error {
	h := interp.HandlerCtx(ctx)
	type resource struct {
		flag byte
		name string
		id   int
		unit uint64
	}
	resources := []resource{
		{'c', "core file size (blocks)", unix.RLIMIT_CORE, 1024},
		{'d', "data segment size (kbytes)", unix.RLIMIT_DATA, 1024},
		{'f', "file size (blocks)", unix.RLIMIT_FSIZE, 1024},
		{'n', "open files", unix.RLIMIT_NOFILE, 1},
		{'s', "stack size (kbytes)", unix.RLIMIT_STACK, 1024},
		{'t', "cpu time (seconds)", unix.RLIMIT_CPU, 1},
		{'v', "virtual memory (kbytes)", unix.RLIMIT_AS, 1024},
	}
	hard, all := false, false
	selected := byte('f')
	fail := func(message string) error {
		fmt.Fprintln(h.Stderr, "ulimit: "+message)
		return interp.ExitStatus(2)
	}
	for _, arg := range args[1:] {
		if !strings.HasPrefix(arg, "-") || arg == "-" || arg == "--" {
			return fail("setting limits is unsupported by the embedded shell; only inspection is available")
		}
		for _, flag := range []byte(arg[1:]) {
			switch flag {
			case 'H':
				hard = true
			case 'S':
				hard = false
			case 'a':
				all = true
			case 'c', 'd', 'f', 'n', 's', 't', 'v':
				selected = flag
			default:
				return fail(fmt.Sprintf("unsupported option -%c", flag))
			}
		}
	}
	for _, resource := range resources {
		if !all && selected != resource.flag {
			continue
		}
		var limit unix.Rlimit
		if err := unix.Getrlimit(resource.id, &limit); err != nil {
			fmt.Fprintln(h.Stderr, "ulimit:", err)
			return interp.ExitStatus(1)
		}
		value := limit.Cur
		if hard {
			value = limit.Max
		}
		if all {
			fmt.Fprintf(h.Stdout, "%s (-%c) ", resource.name, resource.flag)
		}
		if value == unix.RLIM_INFINITY {
			fmt.Fprintln(h.Stdout, "unlimited")
		} else {
			fmt.Fprintln(h.Stdout, value/resource.unit)
		}
	}
	return nil
}

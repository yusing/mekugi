package launch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

const testNativeModelCatalog = `{"models":[{"slug":"gpt-5.6-sol","tool_mode":"code_mode","multi_agent_version":"v2","shell_type":"unified_exec","apply_patch_tool_type":"freeform","model_messages":{"instructions_template":"native instructions"}}]}`

func TestCatalogCommandProcess(t *testing.T) {
	if os.Getenv("MEKUGI_TEST_CATALOG_COMMAND") != "1" {
		return
	}
	switch os.Getenv("MEKUGI_TEST_CATALOG_MODE") {
	case "fail":
		fmt.Fprintln(os.Stderr, "private bootstrap diagnostic")
		os.Exit(2)
	case "invalid":
		fmt.Fprintln(os.Stdout, "not JSON")
	case "missing_template":
		fmt.Fprintln(os.Stdout, `{"models":[{"slug":"legacy"}]}`)
	case "oversized":
		fmt.Fprint(os.Stdout, strings.Repeat(" ", (8<<20)+1))
	case "wait":
		if err := os.WriteFile(os.Getenv("MEKUGI_TEST_CATALOG_READY"), nil, 0o600); err != nil {
			os.Exit(3)
		}
		time.Sleep(time.Minute)

	default:
		if path := os.Getenv("MEKUGI_TEST_CATALOG_ARGS"); path != "" {
			cwd, _ := os.Getwd()
			data, _ := json.Marshal(struct {
				Args []string
				Cwd  string
			}{os.Args[slices.Index(os.Args, "--")+1:], cwd})
			if err := os.WriteFile(path, data, 0o600); err != nil {
				os.Exit(3)
			}
		}
		fmt.Fprintln(os.Stdout, testNativeModelCatalog)
	}
	os.Exit(0)
}

func catalogTestExecutable(t *testing.T) string {
	t.Helper()
	t.Setenv("MEKUGI_TEST_CATALOG_COMMAND", "1")
	path := filepath.Join(t.TempDir(), "codex")
	stub := "#!/bin/sh\nexec " + strconv.Quote(os.Args[0]) + " -test.run=^TestCatalogCommandProcess$ -- \"$@\"\n"
	if err := os.WriteFile(path, []byte(stub), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

// Cancel only once the actual bootstrap child has started waiting. The deadline
// is failure headroom, not a delay paid by every successful test.
func cancelWhenCatalogReady(t *testing.T) context.Context {
	t.Helper()
	ready := filepath.Join(t.TempDir(), "ready")
	t.Setenv("MEKUGI_TEST_CATALOG_READY", ready)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	t.Cleanup(cancel)
	go func() {
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if _, err := os.Stat(ready); err == nil {
					cancel()
					return
				}
			}
		}
	}()
	return ctx
}

func TestBundledModelCatalog(t *testing.T) {
	for _, mode := range []string{"ok", "fail", "oversized", "wait"} {
		t.Run(mode, func(t *testing.T) {
			executable := catalogTestExecutable(t)
			t.Setenv("MEKUGI_TEST_CATALOG_MODE", mode)
			ctx := t.Context()
			if mode == "wait" {
				ctx = cancelWhenCatalogReady(t)
			}
			body, err := bundledModelCatalog(ctx, executable, t.TempDir())
			if mode == "ok" {
				if err != nil || strings.TrimSpace(string(body)) != testNativeModelCatalog {
					t.Fatalf("catalog: %s %v", body, err)
				}
				return
			}
			if err == nil || strings.Contains(err.Error(), "private bootstrap") {
				t.Fatalf("unsafe bootstrap error: %v", err)
			}
			if mode == "wait" && !errors.Is(err, context.Canceled) {
				t.Fatalf("lost cancellation: %v", err)
			}
		})
	}
}

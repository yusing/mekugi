package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

const testNativeModelCatalog = `{"models":[{"slug":"gpt-5.6-sol","multi_agent_version":"v2","shell_type":"unified_exec","apply_patch_tool_type":"freeform","model_messages":{"instructions_template":"native instructions"}}]}`

func TestCatalogConfigArgs(t *testing.T) {
	tests := []struct {
		name       string
		args, want []string
		cwd        string
	}{
		{"resume", []string{"resume", "thread", "-c", `model_catalog_json="chosen.json"`, "-C", "/tmp/work", "--", "-p", "ignored"},
			[]string{"-c", `model_catalog_json="chosen.json"`}, "/tmp/work"},
		{"inline", []string{"exec", "--config=model=x", `-c=foo="old"`, "--cd=/tmp/next"},
			[]string{"-c", "model=x", "-c", `foo="old"`}, "/tmp/next"},
		{"short", []string{"-cfoo=1", "-C/tmp", "prompt"},
			[]string{"-c", "foo=1"}, "/tmp"},
		{"long", []string{"--config", "foo=1", "--cd", "/tmp"},
			[]string{"-c", "foo=1"}, "/tmp"},
		{"plain", []string{"exec", "a prompt with spaces"}, nil, ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, cwd, err := catalogConfigArgs(test.args)
			if err != nil || !slices.Equal(got, test.want) || cwd != test.cwd {
				t.Fatalf("catalog selectors = %v, %q; want %v, %q", got, cwd, test.want, test.cwd)
			}
		})
	}
}
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

func TestPrepareGrokCatalog(t *testing.T) {
	executable := catalogTestExecutable(t)
	record := filepath.Join(t.TempDir(), "args.json")
	t.Setenv("MEKUGI_TEST_CATALOG_ARGS", record)
	cwd := t.TempDir()
	directory, path, err := prepareGrokCatalog(t.Context(), executable, "http://127.0.0.1:12345/v1",
		[]string{"resume", "thread", "-C", cwd, "-c", `model_catalog_json="custom.json"`, "--", "prompt"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(directory) })
	for file, mode := range map[string]os.FileMode{directory: 0o700, path: 0o600} {
		info, err := os.Stat(file)
		if err != nil || info.Mode().Perm() != mode {
			t.Fatalf("private catalog permissions for %s: %v, %v", file, info, err)
		}
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var catalog struct {
		Models []map[string]any
	}
	if err := json.Unmarshal(body, &catalog); err != nil || len(catalog.Models) != 2 {
		t.Fatalf("catalog models: %v, %v", catalog.Models, err)
	}
	grok := catalog.Models[1]
	if grok["slug"] != "grok:grok-4.6" || grok["apply_patch_tool_type"] != "freeform" || grok["shell_type"] != "unified_exec" {
		t.Fatalf("lost Grok execution metadata: %v", grok)
	}
	var recorded struct {
		Args []string
		Cwd  string
	}
	data, err := os.ReadFile(record)
	if err != nil || json.Unmarshal(data, &recorded) != nil {
		t.Fatalf("read command record: %v", err)
	}
	want := codexArgs("http://127.0.0.1:12345/v1", []string{"debug", "models", "-c", `model_catalog_json="custom.json"`}, false)
	if !slices.Equal(recorded.Args, want) || recorded.Cwd != cwd {
		t.Fatalf("debug models arguments = %v, cwd %q", recorded.Args, recorded.Cwd)
	}
}

func TestPrepareGrokCatalogFailure(t *testing.T) {
	for _, mode := range []string{"fail", "invalid", "missing_template", "oversized", "wait"} {
		t.Run(mode, func(t *testing.T) {
			executable := catalogTestExecutable(t)
			t.Setenv("MEKUGI_TEST_CATALOG_MODE", mode)
			temp := t.TempDir()
			t.Setenv("TMPDIR", temp)
			ctx := t.Context()
			if mode == "wait" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 2*time.Second)
				defer cancel()
			}
			directory, path, err := prepareGrokCatalog(ctx, executable, "http://127.0.0.1:12345/v1", nil)
			if err == nil || directory != "" || path != "" || strings.Contains(err.Error(), "private bootstrap diagnostic") {
				t.Fatalf("failure result = %q, %q, %v", directory, path, err)
			}
			if entries, err := os.ReadDir(temp); err != nil || len(entries) != 0 {
				t.Fatalf("failed catalog leaked files: %v, %v", entries, err)
			}
		})
	}
}

func TestGrokCatalogRejectsNamedProfiles(t *testing.T) {
	for _, args := range [][]string{
		{"-p", "work"}, {"resume", "thread", "--profile", "work"},
		{"exec", "--profile=work", "prompt"}, {"-pwork"}, {"-p=work"}, {"-p"}, {"--profile"},
	} {
		_, _, err := catalogConfigArgs(args)
		if err == nil || !strings.Contains(err.Error(), "--grok does not support --profile") {
			t.Fatalf("accepted named profile %v: %v", args, err)
		}
	}
}

func TestPrepareGrokCatalogUsesAbsolutePath(t *testing.T) {
	executable := catalogTestExecutable(t)
	t.Chdir(t.TempDir())
	if err := os.Mkdir("temporary", 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", "temporary")
	cwd := t.TempDir()
	directory, path, err := prepareGrokCatalog(t.Context(), executable, "http://127.0.0.1:12345/v1", []string{"-C", cwd})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(directory) })
	if !filepath.IsAbs(directory) || !filepath.IsAbs(path) {
		t.Fatalf("catalog path depends on Codex's working directory: %q, %q", directory, path)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

func TestWrapGrokCatalogFailureCleansRuntime(t *testing.T) {
	executable := catalogTestExecutable(t)
	t.Setenv("MEKUGI_TEST_CATALOG_MODE", "fail")
	t.Setenv("PATH", filepath.Dir(executable)+string(os.PathListSeparator)+os.Getenv("PATH"))
	temporary := t.TempDir()
	runtime := t.TempDir()
	t.Setenv("TMPDIR", temporary)
	t.Setenv("MEKUGI_RUNTIME_DIR", runtime)
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	code, err := wrapCodex(ctx, []string{"--grok", "--model-protocol", "native"}, []string{"exec", "prompt"})
	if code != 1 || err == nil || !strings.Contains(err.Error(), "codex debug models failed") {
		t.Fatalf("catalog bootstrap failure = %d, %v", code, err)
	}
	for _, path := range []string{temporary, runtime} {
		if entries, err := os.ReadDir(path); err != nil || len(entries) != 0 {
			t.Fatalf("bootstrap failure leaked runtime files: %v, %v", entries, err)
		}
	}
}

func TestGrokCatalogRejectsIgnoreUserConfigBeforeBootstrap(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "missing-codex")
	for _, args := range [][]string{
		{"exec", "--ignore-user-config", "prompt"},
		{"exec", "--ignore-user-config=true", "prompt"},
	} {
		_, _, err := prepareGrokCatalog(t.Context(), executable, "http://127.0.0.1:12345/v1", args)
		if err == nil || !strings.Contains(err.Error(), "--grok does not support --ignore-user-config") {
			t.Fatalf("did not reject configuration selector before bootstrap: %v", err)
		}
	}
	if _, _, err := catalogConfigArgs([]string{"exec", "--", "--ignore-user-config"}); err != nil {
		t.Fatalf("treated prompt data as a configuration selector: %v", err)
	}
}

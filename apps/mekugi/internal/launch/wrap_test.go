package launch

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	launchoptions "github.com/yusing/mekugi/apps/mekugi/internal/launch/options"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/modules/threadcontrol/control"

	"github.com/yusing/mekugi/libraries/commandargs"

	"github.com/yusing/mekugi/backends/codex/binding"

	segmenttransport "github.com/yusing/mekugi/modules/commandsegments/transport"

	"github.com/yusing/mekugi/libraries/shellinstrumentation"

	"github.com/yusing/mekugi/backends/codex/shellapproval"
)

func TestServerInvocationRequiresExecutionOwners(t *testing.T) {
	for _, execution := range []*threadExecution{
		nil,
		{},
		{shared: control.New(), start: &sessioncontrol.PreparedThread{}},
	} {
		code, err := runOwnedInvocation(t.Context(), nil, nil, sessionapi.NewLive(), t.TempDir(), execution)
		if code != 1 || err == nil {
			t.Fatalf("incomplete server owners admitted: code=%d error=%v", code, err)
		}
	}
}

func TestCodexArgsPreservesArguments(t *testing.T) {
	for _, forwarded := range [][]string{
		{"-c", `model="example"`, "--", "a prompt with spaces"},
		{"-c", `model="example"`},
	} {
		original := slices.Clone(forwarded)
		args := codexArgs("http://127.0.0.1:12345/v1", forwarded, nil)
		index := slices.Index(forwarded, "--")
		if index < 0 {
			index = len(forwarded)
		}
		delimiter := slices.Index(args, "--")
		if delimiter < 0 {
			delimiter = len(args)
		}
		if !slices.Equal(args[:index], forwarded[:index]) || !slices.Equal(args[delimiter:], forwarded[index:]) || !slices.Equal(forwarded, original) {
			t.Fatalf("caller arguments changed: %q", args)
		}
		values := launchoptions.ConfigOverrides(args)
		if values["model_provider"] != "openai" || values["openai_base_url"] != "http://127.0.0.1:12345/v1" || values["model"] != "example" {
			t.Fatalf("provider endpoint or model changed: %#v", values)
		}
	}
}

// Discover installed native shells without live session guards.
func frontendFixtureShell(name string) (string, error) {
	for _, directory := range filepath.SplitList(os.Getenv("PATH")) {
		if filepath.Base(directory) == shellinstrumentation.VCSDirectory {
			continue
		}
		candidate, err := filepath.Abs(filepath.Join(directory, name))
		if err != nil {
			return "", err
		}
		if path, err := exec.LookPath(candidate); err == nil {
			if resolved, err := filepath.EvalSymlinks(path); err == nil && filepath.Base(resolved) == "mise" {
				continue
			}
			return path, nil
		}
	}
	return "", exec.ErrNotFound
}

func TestFrontendPathSurvivesLoginBash(t *testing.T) {
	bash, err := frontendFixtureShell("bash")
	if err != nil {
		t.Skip("bash unavailable")
	}
	snapshot := filepath.Join(t.TempDir(), "snapshot with spaces")
	frontend := filepath.Join(snapshot, "bin")
	if err := os.MkdirAll(frontend, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(frontend, "mcat"), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	installed := filepath.Join(snapshot, "user's [tools]")
	if err := os.Mkdir(installed, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(installed, "mekugi"), []byte("#!/bin/sh\nprintf '%s\\n' \"$*\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	prior := filepath.Join(snapshot, "prior-bash-env")
	// Model a profile replacing PATH once, then source startup again to check
	// that restoring inherited entries does not keep growing the path.
	if err := os.WriteFile(prior, []byte("if [ -z \"${MEKUGI_PRIOR_BASH_ENV-}\" ]; then PATH=/usr/bin:/bin; fi\nexport MEKUGI_PRIOR_BASH_ENV=preserved\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	environment, err := frontendShellEnvironment([]string{"PATH=" + installed + ":/usr/bin:/bin", "BASH_ENV=" + prior}, frontend, "", nil, segmenttransport.Endpoints{})
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"-c", "-lc"} {
		command := exec.Command(bash, mode, "command -v mcat; mekugi inspect-session --help; . \"$BASH_ENV\"; printf '%s\\n%s' \"$MEKUGI_PRIOR_BASH_ENV\" \"$PATH\"")
		command.Env = environment
		output, err := command.Output()
		prefix := filepath.Join(frontend, "mcat") + "\ninspect-session --help\npreserved\n"
		path, ok := strings.CutPrefix(string(output), prefix)
		if err != nil || !ok || strings.Count(":"+path+":", ":"+installed+":") != 1 {
			t.Fatalf("Bash %s frontend = %q, %v", mode, output, err)
		}
	}
}

func TestFrontendShellEnvironmentGuardsBash(t *testing.T) {
	frontend := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(frontend, 0o700); err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(t.TempDir(), "mekugi-exec")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	environment, err := frontendShellEnvironment([]string{"PATH=/usr/bin:/bin"}, frontend, helper, testShellGuards(true), segmenttransport.Endpoints{Requests: filepath.Join(t.TempDir(), "requests"), Directory: filepath.Join(t.TempDir(), "reports")})
	if err != nil {
		t.Fatal(err)
	}
	guard, _ := shellapproval.Paths(frontend)
	values := map[string]string{}
	for _, entry := range environment {
		key, value, _ := strings.Cut(entry, "=")
		values[key] = value
	}
	if !strings.HasPrefix(values["PATH"], guard+":"+frontend+":") {
		t.Fatalf("PATH = %q", values["PATH"])
	}
	bashEnv, err := os.ReadFile(values["BASH_ENV"])
	if err != nil || !strings.Contains(string(bashEnv), "__mekugi_vcs_paths") {
		t.Fatalf("BASH_ENV = %q, %v", bashEnv, err)
	}
}

func TestFrontendScriptSudo(t *testing.T) {
	for _, guard := range []bool{false, true} {
		t.Run(fmt.Sprint(guard), func(t *testing.T) {
			root := t.TempDir()
			frontend, real, custom := filepath.Join(root, "bin"), filepath.Join(root, "real"), filepath.Join(root, "custom")
			for _, directory := range []string{frontend, real, custom} {
				if err := os.Mkdir(directory, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			helper := filepath.Join(root, "mekugi-exec")
			prior := filepath.Join(root, "user-bash-env")
			for path, content := range map[string]string{
				helper:                        "#!/bin/sh\ncase ${0##*/} in sudo|git) printf '%s <%s>\\n' \"$MEKUGI_VCS_GUARD_REAL\" \"$1\";; esac\n",
				filepath.Join(real, "sudo"):   "#!/bin/sh\necho BYPASSED\n",
				filepath.Join(real, "git"):    "#!/bin/sh\necho BYPASSED\n",
				filepath.Join(custom, "sudo"): "#!/bin/sh\necho BYPASSED\n",
				prior:                         filepath.Join(custom, "sudo") + "() { printf 'CUSTOM\\n'; }\n",
			} {
				if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			environment, err := frontendShellEnvironment([]string{"PATH=" + real + ":" + custom + ":/usr/bin:/bin", "BASH_ENV=" + prior}, frontend, helper, testShellGuards(guard), segmenttransport.Endpoints{Requests: filepath.Join(t.TempDir(), "requests"), Directory: filepath.Join(t.TempDir(), "reports")})
			if err != nil {
				t.Fatal(err)
			}
			script := filepath.Join(root, "script")
			body := "#!/bin/bash\n. \"$BASH_ENV\"\nsudo 'bare argument'\n" + commandargs.Quote(filepath.Join(real, "sudo")) + " 'two words'\n" + commandargs.Quote(filepath.Join(real, "git")) + " push\n" + commandargs.Quote(filepath.Join(custom, "sudo")) + "\nexit 7\n"
			if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
				t.Fatal(err)
			}
			for _, args := range [][]string{{"/bin/bash", script}, {script}} {
				cmd := exec.Command(args[0], args[1:]...)
				cmd.Env = environment
				out, err := cmd.CombinedOutput()
				want := "BYPASSED\nBYPASSED\nBYPASSED\n"
				if guard {
					want = " <bare argument>\n" + filepath.Join(real, "sudo") + " <two words>\n" + filepath.Join(real, "git") + " <push>\n"
				}
				want += "CUSTOM\n"
				if err == nil || cmd.ProcessState.ExitCode() != 7 || string(out) != want {
					t.Fatalf("script=%v output=%q err=%v; want %q and exit 7", args, out, err, want)
				}
			}
		})
	}
}

func TestWrapCodexMissingExecutable(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	code, err := runServerSession(t.Context(), nil, nil, sessionapi.NewLive(), t.TempDir(), &threadExecution{shared: control.New()})
	if code != 1 || err == nil || !strings.Contains(err.Error(), "locate mekugi-app-server") {
		t.Fatalf("wrap = %d, %v", code, err)
	}
}

func TestValidateCodexArgs(t *testing.T) {
	for _, args := range [][]string{
		{"--oss"}, {"exec", "--local-provider", "ollama"}, {"--local-provider=ollama"},
		{"-c", `model_provider="other"`}, {"--config=model_providers.other={}"},
		{`-cmodel_provider="other"`}, {`-c=model_provider="other"`},
		{"--config", `"model_providers".mekugi_wrap.base_url="https://example.com"`},
		{"-c", `openai_base_url="https://example.com"`}, {"-c", `oss_provider="ollama"`},
	} {
		if err := validateCodexArgs(args, false); err == nil {
			t.Errorf("accepted provider override: %q", args)
		}
	}
	for _, args := range [][]string{
		nil, {"exec", "a prompt with spaces"}, {"--profile", "work"},
		{"exec", "-c", `model="example"`}, {"--", "--oss"},
		{"--", `-cmodel_provider="other"`}, {"-c"},
	} {
		if err := validateCodexArgs(args, false); err != nil {
			t.Errorf("rejected ordinary arguments %q: %v", args, err)
		}
	}
}

func TestWrapRejectsRawAndRedirectedInvocations(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	for _, args := range [][]string{
		nil, {"--yolo"}, {"resume", "saved"},
		{"exec", "prompt"}, {"e", "prompt"}, {"review"},
		{"login"}, {"logout"}, {"mcp", "list"}, {"mcp-server"},
		{"app-server"}, {"app"}, {"completion"}, {"sandbox"},
		{"debug", "models"}, {"apply"}, {"a"}, {"cloud"}, {"features"}, {"help"},
		{"-pwork", "login"}, {"--model", "login", "exec", "prompt"},
	} {
		code, err := wrapCodex(t.Context(), nil, args)
		if code != 2 || err == nil || !strings.Contains(err.Error(), "headless --yolo") || strings.Contains(err.Error(), "locate") {
			t.Errorf("unsupported invocation %q = %d, %v", args, code, err)
		}
	}
}

func TestRunServerRequiresCustomProvider(t *testing.T) {
	for _, key := range []string{"HOME", "CODEX_HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME"} {
		t.Setenv(key, t.TempDir())
	}
	t.Setenv("PATH", t.TempDir())
	code, err := runServerSession(t.Context(), []string{"custom-provider"}, nil, sessionapi.NewLive(), t.TempDir(), &threadExecution{shared: control.New()})
	if code != 2 || err == nil || !strings.Contains(err.Error(), "requires a custom Responses provider profile") {
		t.Fatalf("standalone without provider = %d, %v", code, err)
	}
}

func TestWrapCodexStartupFailures(t *testing.T) {
	for _, failure := range []string{"router", "codex"} {
		t.Run(failure, func(t *testing.T) {
			directory := t.TempDir()
			t.Setenv("TMPDIR", t.TempDir())
			runtimeDirectory := t.TempDir()
			configDirectory := t.TempDir()
			t.Setenv("XAI_API_KEY", "test")
			t.Setenv("CODEX_HOME", t.TempDir())
			t.Setenv("HOME", t.TempDir())
			t.Setenv("XDG_CONFIG_HOME", configDirectory)
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			t.Setenv("MEKUGI_RUNTIME_DIR", runtimeDirectory)
			t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
			marker := filepath.Join(directory, "launched")
			stub := "#!/bin/sh\ntouch " + strconv.Quote(marker) + "\n"
			if failure == "codex" {
				stub = "#!/nonexistent-mekugi-test-interpreter\n"
			} else {
				statePath := filepath.Join(directory, "state-file")
				if err := os.WriteFile(statePath, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				t.Setenv("XDG_STATE_HOME", statePath)
			}
			if err := os.WriteFile(filepath.Join(directory, "mekugi-app-server"), []byte(stub), 0o700); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			code, err := runServerSession(ctx, []string{"--shell-guard=false"}, nil, sessionapi.NewLive(), t.TempDir(), &threadExecution{shared: control.New()})
			if code != 1 || err == nil {
				t.Fatalf("wrap = %d, %v", code, err)
			}
			if failure == "router" && !strings.Contains(err.Error(), "initialize replay storage") {
				t.Fatalf("unexpected router startup failure: %v", err)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Errorf("Codex launched despite startup failure: %v", err)
			}
			entries, err := os.ReadDir(runtimeDirectory)
			if err != nil || len(entries) != 0 {
				t.Errorf("runtime resources survived: %v, %v", entries, err)
			}
		})
	}
}

func TestFrontendShellEnvironmentWithoutGuardKeepsTracking(t *testing.T) {
	bash, err := frontendFixtureShell("bash")
	if err != nil {
		t.Skip("bash unavailable")
	}
	root := t.TempDir()
	frontend := filepath.Join(root, "bin")
	if err := os.Mkdir(frontend, 0o700); err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(root, "mekugi-exec")
	log := filepath.Join(root, "tracked-script")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nprintf '%s\\n' \"$3\" > \"$MEKUGI_TEST_TRACK_LOG\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	environment, err := frontendShellEnvironment([]string{"PATH=" + os.Getenv("PATH"), "MEKUGI_TEST_TRACK_LOG=" + log, "CODEX_THREAD_ID=outer-thread", shellinstrumentation.Guard + "=1", shellinstrumentation.ShTrackerEnvironment + "=/outer/exec-track.sh"}, frontend, helper, nil, segmenttransport.Endpoints{Requests: filepath.Join(t.TempDir(), "requests"), Directory: filepath.Join(t.TempDir(), "reports")})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(environment, "PATH="+frontend+":"+os.Getenv("PATH")) {
		t.Fatalf("unguarded environment = %q", environment)
	}
	if slices.Contains(environment, shellinstrumentation.Guard+"=1") || !slices.Contains(environment, shellinstrumentation.ShTrackerEnvironment+"="+filepath.Join(root, "exec-track.sh")) {
		t.Fatalf("new session inherited shell tracking state: %q", environment)
	}
	if slices.Contains(environment, "CODEX_THREAD_ID=outer-thread") {
		t.Fatal("new session inherited command identity")
	}
	guard, _ := shellapproval.Paths(frontend)
	if _, err := os.Stat(guard); !os.IsNotExist(err) {
		t.Fatalf("unguarded shell installed guard: %v", err)
	}
	if _, err := os.Stat(shellapproval.SudoDirectory(frontend)); !os.IsNotExist(err) {
		t.Fatalf("unguarded shell installed sudo helpers: %v", err)
	}
	for _, entry := range environment {
		if strings.HasPrefix(entry, shellapproval.DirectoryEnvironment+"=") {
			t.Fatal("unguarded shell exposed sudo instrumentation")
		}
	}
	const script = "printf local; printf read"
	// The host's hook shell inherits the launcher environment, not the tool's
	// thread injection. It must execute without entering command tracking.
	hook := exec.Command(bash, "-c", script)
	hook.Env = environment
	if output, err := hook.CombinedOutput(); err != nil || string(output) != "localread" {
		t.Fatalf("hook command = %q, %v", output, err)
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatalf("hook shell started command tracking: %v", err)
	}
	command := exec.Command(bash, "-c", script)
	command.Env = append(slices.Clone(environment), "CODEX_THREAD_ID=tool-thread")
	if output, err := command.CombinedOutput(); err != nil || string(output) != "localread" {
		t.Fatalf("unguarded command = %q, %v", output, err)
	}
	compatible := exec.Command(bash, "-c", `(( BASH_VERSINFO[0] > 4 || (BASH_VERSINFO[0] == 4 && BASH_VERSINFO[1] >= 1) ))`)
	compatible.Env = []string{"PATH=" + os.Getenv("PATH")}
	if err := compatible.Run(); err != nil {
		if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 {
			t.Fatalf("check Bash tracking support: %v", err)
		}
		if _, err := os.Stat(log); !os.IsNotExist(err) {
			t.Fatalf("unsupported Bash started tracking: %v", err)
		}
		return
	}
	if tracked, err := os.ReadFile(log); err != nil || string(tracked) != script+"\n" {
		t.Fatalf("tracked script = %q, %v", tracked, err)
	}
}

func TestFrontendNestedGuardOptOut(t *testing.T) {
	root := t.TempDir()
	outer, inner, real := filepath.Join(root, "outer", "bin"), filepath.Join(root, "inner", "bin"), filepath.Join(root, "user", shellinstrumentation.VCSDirectory)
	for _, path := range []string{outer, inner, real} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	helper, userEnv := filepath.Join(root, "mekugi-exec"), filepath.Join(root, "user-bash-env")
	for path, script := range map[string]string{
		helper:                     "#!/bin/sh\nprintf OUTER_GUARD\n",
		filepath.Join(real, "git"): "#!/bin/sh\nprintf REAL_GIT\n",
		userEnv:                    "export USER_STARTUP=preserved\n",
	} {
		if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	env := []string{"PATH=" + real + ":/usr/bin:/bin", "BASH_ENV=" + userEnv}
	env, err := frontendShellEnvironment(env, outer, helper, testShellGuards(true), segmenttransport.Endpoints{Requests: filepath.Join(t.TempDir(), "requests"), Directory: filepath.Join(t.TempDir(), "reports")})
	if err != nil {
		t.Fatal(err)
	}
	env, err = frontendShellEnvironment(env, inner, "", nil, segmenttransport.Endpoints{})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(env, "PATH="+inner+":"+real+":/usr/bin:/bin") {
		t.Fatalf("nested PATH retains stale frontend: %q", env)
	}
	for _, name := range []string{"git", filepath.Join(real, "git")} {
		cmd := exec.Command("/bin/bash", "-c", commandargs.Quote(name)+" push; printf ' %s' \"$USER_STARTUP\"")
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil || string(out) != "REAL_GIT preserved" {
			t.Fatalf("nested opt-out %q = %q, %v", name, out, err)
		}
	}
}

func TestInheritedFrontendEnvironment(t *testing.T) {
	root := t.TempDir()
	startup := filepath.Join(root, "frontend-bash-env")
	for _, marked := range []bool{false, true} {
		body := "# user startup\n"
		if marked {
			body = frontendBashEnvMarker
		}
		if err := os.WriteFile(startup, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		for _, previous := range []string{"", "/user/startup"} {
			environment := []string{"PATH=" + root + "/bin:" + root + "/vcs:" + root + "/sudo:" + root + "-neighbor/bin:/usr/bin", "BASH_ENV=" + startup, userBashEnvEnvironment + "=" + previous, "KEEP=value", "TERM=dumb", "TMUX=outer", "HERDR_ENV=1", "MEKUGI_BASE_URL=outer", "MEKUGI_RUNTIME_DIR=outer"}
			for _, key := range []string{"CODEX_THREAD_ID", shellinstrumentation.Guard, shellinstrumentation.ShTrackerEnvironment, shellapproval.DirectoryEnvironment, shellinstrumentation.ItemEnvironment, shellinstrumentation.RealEnvironment} {
				environment = append(environment, key+"=outer")
			}
			want := slices.Clone(environment[:9])
			want = slices.Delete(want, 2, 3)
			if marked {
				want[0] = "PATH=" + root + "-neighbor/bin:/usr/bin"
				want = slices.Delete(want, 1, 2)
				if previous != "" {
					want = append(want[:1], append([]string{"BASH_ENV=" + previous}, want[1:]...)...)
				}
			}
			if got := withoutInheritedFrontend(environment); !slices.Equal(got, want) {
				t.Fatalf("marked=%v previous=%q: scrubbed=%q want=%q", marked, previous, got, want)
			}
			serverWant := slices.DeleteFunc(slices.Clone(want), func(entry string) bool {
				return strings.HasPrefix(entry, "TERM=") || strings.HasPrefix(entry, "TMUX=") || strings.HasPrefix(entry, "HERDR_") || strings.HasPrefix(entry, "MEKUGI_BASE_URL=") || strings.HasPrefix(entry, "MEKUGI_RUNTIME_DIR=")
			})
			if got := serverEnvironment(environment); !slices.Equal(got, append(serverWant, "TERM=xterm-256color")) {
				t.Fatalf("server environment=%q", got)
			}
		}
	}
	for _, terminal := range []string{"", "dumb", "screen-256color"} {
		want := terminal
		if want == "" || want == "dumb" {
			want = "xterm-256color"
		}
		if got := serverEnvironment([]string{"TERM=" + terminal}); !slices.Equal(got, []string{"TERM=" + want}) {
			t.Fatalf("terminal %q: %q", terminal, got)
		}
	}
}

func testShellGuards(enabled bool) []binding.ShellApproval {
	if !enabled {
		return nil
	}
	return []binding.ShellApproval{{Kind: binding.RemoteWrite}, {Kind: binding.Sudo}}
}

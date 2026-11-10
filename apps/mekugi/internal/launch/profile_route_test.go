package launch

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	launchoptions "github.com/yusing/mekugi/apps/mekugi/internal/launch/options"
)

func TestProfileRouteRetainsProviderAndNativeAuth(t *testing.T) {
	home := t.TempDir()
	body := `model="opencode-go:literal"
model_provider="corp.gateway"
model_context_window=123456
model_auto_compact_token_limit=120000
model_reasoning_effort="high"
[model_providers."corp.gateway"]
name="Native provider"
base_url="https://example.invalid/prefix"
wire_api="responses"
supports_websockets=true
requires_openai_auth=false
websocket_connect_timeout_ms=20000
stream_idle_timeout_ms=900000
auth={command="token-helper",args=["--literal","space value"],timeout_ms=2000}
query_params={api_version="a%2Bb"}
http_headers={"X-Provider"="fixture"}
capabilities={remote_compaction="v2",cyber_access_programs=false}
`
	if err := os.WriteFile(filepath.Join(home, "work.config.toml"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	profile, args, err := launchoptions.ImportProfile([]string{"-pwork", "exec", "-m", "grok:literal", "--", "prompt"}, []string{"CODEX_HOME=" + home})
	if err != nil {
		t.Fatal(err)
	}
	route, err := profileRoute(args, profile)
	if err != nil {
		t.Fatal(err)
	}
	if route.ID != "corp.gateway" || route.BaseURL != "https://example.invalid/prefix" || !route.WebSockets || route.StreamIdleTimeout.Milliseconds() != 900000 {
		t.Fatalf("wrong native route: %+v", route)
	}
	before := launchoptions.ConfigOverrides(args)
	projected := codexArgs("http://127.0.0.1:1234/v1", args, false, false, route)
	after := launchoptions.ConfigOverrides(projected)
	providers := before["model_providers"].(map[string]any)
	providers[route.ID].(map[string]any)["base_url"] = "http://127.0.0.1:1234/v1"
	if !reflect.DeepEqual(providers, after["model_providers"]) || after["model_provider"] != route.ID || after["model"] != "grok:literal" {
		t.Fatalf("provider definition/auth/model changed: %#v", after)
	}
	if _, exists := after["openai_base_url"]; exists {
		t.Fatal("injected unrelated OpenAI endpoint")
	}
	if !strings.HasSuffix(strings.Join(projected, "\n"), "--\nprompt") {
		t.Fatal("prompt/delimiter changed")
	}
	continuation := appServerResumeArgv("mekugi", nil, []string{"-p", "work", "-m", "grok:literal", "resume", "saved"})
	if strings.Contains(strings.Join(continuation, " "), "127.0.0.1") || !strings.Contains(strings.Join(continuation, " "), "-p work") || strings.Contains(strings.Join(continuation, " "), "saved") {
		t.Fatalf("continuation: %q", continuation)
	}
}

func TestProfileRouteAdmissionAndIntegrationConflicts(t *testing.T) {
	p := &launchoptions.Profile{Name: "work"}
	for _, setting := range []string{
		`model_providers={custom={name="custom"}}`,
		`model_providers={custom={base_url="https://example.invalid",aws={region="us-east-1"}}}`,
		`model_providers={custom={base_url="https://example.invalid",gateway_oauth={issuer="https://example.invalid"}}}`,
		`model_providers={custom={base_url="https://example.invalid",wire_api="chat"}}`,
		`model_providers={custom={base_url="file:///tmp/provider"}}`,
		`model_providers={custom={base_url="https://user:password@example.invalid"}}`,
	} {
		if _, err := profileRoute([]string{"-c", `model_provider="custom"`, "-c", setting}, p); err == nil {
			t.Fatalf("admitted incompatible %s", setting)
		}
	}
	if _, err := profileRoute([]string{"-c", `model_provider="openai"`, "-c", `model_providers={openai={model_catalog_url="https://chatgpt.com/catalog"}}`}, p); err == nil {
		t.Fatal("admitted built-in OpenAI loopback catalog")
	}
	for _, args := range [][]string{{"--disable", "code_mode"}, {"--enable=sleep_tool"}, {"-c", "features.code_mode_only=false"}, {"-c", "features.token_budget.enabled=false"}, {"-c", "features.token_budget.use_history_notes_extension=true"}, {"-c", "features.multi_agent_v2.disable_native_tools=false"}} {
		if err := validateProfileIntegration(args); err == nil {
			t.Fatalf("accepted integration conflict %q", args)
		}
	}
	if route, err := profileRoute([]string{"-c", `model="literal"`}, p); err != nil || route != nil {
		t.Fatalf("profile without selection changed route: %+v %v", route, err)
	}
	if err := validateProfileIntegration([]string{"-c", "features.code_mode=true", "--", "--disable=code_mode"}); err != nil {
		t.Fatal(err)
	}
}

func TestProfilePrivateCatalogPreservesSource(t *testing.T) {
	workspace := t.TempDir()
	source := filepath.Join(workspace, "source.json")
	body := `{"models":[{"slug":"arbitrary","tool_mode":"code_mode","future":{"literal.key":42}}]}`
	if err := os.WriteFile(source, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(source, filepath.Join(workspace, "link.json")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"source.json", "link.json"} {
		t.Run(path, func(t *testing.T) {
			args, directory, err := prepareProfileCatalog([]string{"exec", "-c", fmt.Sprintf("model_catalog_json=%q", path)}, workspace)
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(directory)
			derived := launchoptions.ConfigOverrides(args)["model_catalog_json"].(string)
			actual, err := os.ReadFile(derived)
			if err != nil || !strings.Contains(string(actual), `"tool_mode":"code_mode_only"`) || !strings.Contains(string(actual), `"literal.key":42`) {
				t.Fatalf("derived catalog: %s %v", actual, err)
			}
			info, err := os.Stat(derived)
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatalf("catalog permissions: %v %v", info, err)
			}
		})
	}
	if original, _ := os.ReadFile(source); string(original) != body {
		t.Fatal("source catalog changed")
	}
}

func TestProfilePrivateCatalogRejectsFIFOWithoutWriter(t *testing.T) {
	if path := os.Getenv("MEKUGI_TEST_PROFILE_FIFO"); path != "" {
		args, directory, err := prepareProfileCatalog([]string{"-c", fmt.Sprintf("model_catalog_json=%q", path)}, filepath.Dir(path))
		if directory != "" {
			defer os.RemoveAll(directory)
		}
		if err == nil || !strings.Contains(err.Error(), "regular file") || args != nil || directory != "" {
			t.Fatalf("FIFO was not rejected: args=%q directory=%q err=%v", args, directory, err)
		}
		return
	}
	path := filepath.Join(t.TempDir(), "catalog.fifo")
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// A subprocess bounds the regression itself, and CommandContext always
	// kills and joins a blocked reader without supplying a FIFO writer.
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestProfilePrivateCatalogRejectsFIFOWithoutWriter$")
	cmd.Env = append(os.Environ(), "MEKUGI_TEST_PROFILE_FIFO="+path)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("FIFO admission blocked or failed: %v: %s", err, output)
	}
}

func TestProfileEffectiveRouteRejectsManagedSubstitution(t *testing.T) {
	route, err := profileRoute([]string{"-c", `model_provider="custom"`, "-c", `model_providers={custom={base_url="https://example.invalid"}}`}, &launchoptions.Profile{Name: "work"})
	if err != nil {
		t.Fatal(err)
	}
	for _, base := range []string{"https://managed.invalid", "http://127.0.0.1:1234/v1"} {
		err := verifyProfileRoute([]byte(fmt.Sprintf(`{"model_provider":"custom","model_providers":{"custom":{"base_url":%q}}}`, base)), route, "http://127.0.0.1:1234/v1")
		if (err == nil) != (base == "http://127.0.0.1:1234/v1") {
			t.Fatalf("managed policy admission: %v", err)
		}
	}
}

func TestProfileResumeAndEntryPointPrecedence(t *testing.T) {
	p := &launchoptions.Profile{Name: "work", Keys: []string{"model", "model_context_window", "model_auto_compact_token_limit"}}
	args := []string{"codex", "app-server", "-c", `model="explicit"`, "-c", "model_context_window=222222", "-c", "model_auto_compact_token_limit=120000", "-c", `model_provider="custom"`, "-c", `model_providers={custom={base_url="http://127.0.0.1:1/v1"}}`}
	config := profileResumeConfig(args, []string{"mekugi", "codex", "-pwork", "-m", "explicit", "-c", "model_context_window=222222"}, p)
	if config["model"] != "explicit" || config["model_context_window"] != int64(222222) || config["model_auto_compact_token_limit"] != int64(120000) || config["model_provider"] != "custom" {
		t.Fatalf("resume explicit/imported precedence: %#v", config)
	}
	for _, args := range [][]string{{"-pwork", "headless", "--yolo"}, {"headless", "-p", "work", "--yolo"}} {
		got, ok := headlessCommand(args)
		if !ok || strings.Contains(strings.Join(got, " "), "headless") {
			t.Fatalf("headless global selector: %q", got)
		}
	}
	for _, args := range [][]string{{"-pwork", "exec", "resume", "saved"}, {"-pwork", "exec", "fork", "saved"}, {"-pwork", "resume", "saved"}} {
		if !sessionInvocation(args) {
			t.Fatalf("session route missed: %q", args)
		}
	}
	for _, args := range [][]string{{"-pwork", "debug", "models"}, {"-pwork", "login"}, {"-pwork", "mcp", "add", "literal"}} {
		if sessionInvocation(args) {
			t.Fatalf("non-session dispatch changed: %q", args)
		}
	}
}

package launch

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	launchoptions "github.com/yusing/mekugi/apps/mekugi/internal/launch/options"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/backends/codex/shellhook"
	"github.com/yusing/mekugi/libraries/sourceanalysis/sourceparse"
)

// profileRoute selects only imported/explicit routes, never guessed provider
// names or ambient built-in credentials. Ordinary host merging remains intact.
func profileRoute(args []string, profile *launchoptions.Profile) (*inferencegateway.NativeProvider, error) {
	if profile == nil {
		return nil, nil
	}
	values := launchoptions.ConfigOverrides(args)
	id, _ := values["model_provider"].(string)
	if id == "" {
		return nil, nil
	}
	providers, _ := values["model_providers"].(map[string]any)
	definition, _ := providers[id].(map[string]any)
	base, _ := definition["base_url"].(string)
	if id == "openai" {
		if definition["model_catalog_url"] != nil {
			return nil, errors.New("built-in OpenAI model_catalog_url cannot be intercepted; use a custom Responses provider")
		}
		if value, ok := values["openai_base_url"].(string); ok {
			base = value
		}
		if base == "" {
			return nil, nil
		}
	}
	if base == "" {
		return nil, errors.New("selected provider requires an imported or explicit base_url")
	}
	if err := validateProfileEndpoint(base); err != nil {
		return nil, err
	}
	if definition["aws"] != nil || definition["gateway_oauth"] != nil || id == "amazon-bedrock" || id == "amazon-bedrock-runtime" {
		return nil, errors.New("provider aws signing and gateway_oauth are incompatible with inference interception")
	}
	if wire, ok := definition["wire_api"].(string); ok && wire != "responses" {
		return nil, errors.New("profile routing requires wire_api=responses")
	}
	websockets, _ := definition["supports_websockets"].(bool)
	catalog, _ := definition["model_catalog_url"].(string)
	if catalog != "" {
		if err := validateProfileEndpoint(catalog); err != nil {
			return nil, errors.New("invalid provider model_catalog_url")
		}
	}
	return &inferencegateway.NativeProvider{ID: id, BaseURL: base, CatalogURL: catalog, WebSockets: websockets, HeaderNames: profileHeaderNames(definition),
		ConnectTimeout: profileMilliseconds(definition["websocket_connect_timeout_ms"]), StreamIdleTimeout: profileMilliseconds(definition["stream_idle_timeout_ms"])}, nil
}

func profileHeaderNames(definition map[string]any) []string {
	var names []string
	for _, field := range []string{"http_headers", "env_http_headers"} {
		configured, _ := definition[field].(map[string]any)
		for name := range configured {
			names = append(names, name)
		}
	}
	return names
}

func profileMilliseconds(value any) time.Duration {
	if value, ok := value.(int64); ok && value > 0 && value <= int64((time.Duration(1<<63-1))/time.Millisecond) {
		return time.Duration(value) * time.Millisecond
	}
	return 0
}

func validateProfileEndpoint(value string) error {
	u, err := url.Parse(value)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Fragment != "" {
		return errors.New("provider endpoint requires an absolute HTTP(S) URL without userinfo or fragment")
	}
	return nil
}

func appendInvocationOverrides(args []string, overrides ...string) []string {
	index := slices.Index(args, "--")
	if index < 0 {
		index = len(args)
	}
	return slices.Insert(slices.Clone(args), index, overrides...)
}

// prepareProfileCatalog derives a private execution catalog without modifying
// the caller's source or substituting bundled models for unknown model IDs.
func prepareProfileCatalog(args []string, workspace string) ([]string, string, error) {
	values := launchoptions.ConfigOverrides(args)
	path, _ := values["model_catalog_json"].(string)
	if path == "" {
		return args, "", nil
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(workspace, path)
	}
	file, err := sourceparse.Open(path)
	if err != nil {
		return nil, "", errors.New("cannot read profile model_catalog_json")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, "", errors.New("profile model_catalog_json must be a regular file")
	}
	body, err := io.ReadAll(io.LimitReader(file, inferencegateway.MaxCatalogBytes+1))
	if err != nil || len(body) > inferencegateway.MaxCatalogBytes {
		return nil, "", errors.New("profile model catalog is unreadable or exceeds 8 MiB")
	}
	body, err = binding.CodeModeOnlyModelCatalog(body)
	if err != nil {
		return nil, "", errors.New("profile model catalog cannot expose stock JavaScript execution")
	}
	directory, err := os.MkdirTemp("", "mekugi-models-")
	if err != nil {
		return nil, "", err
	}
	derived := filepath.Join(directory, "models.json")
	if err := os.WriteFile(derived, body, 0600); err != nil {
		return nil, "", errors.Join(err, os.RemoveAll(directory))
	}
	return appendInvocationOverrides(args, "-c", fmt.Sprintf("model_catalog_json=%q", derived)), directory, nil
}

func profileHTTPClient(route *inferencegateway.NativeProvider) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// Explicit host setup timeouts must not be cut short by the ordinary
	// five-second gateway dial default. The operation context is the bound.
	transport.DialContext = (&net.Dialer{KeepAlive: 30 * time.Second}).DialContext
	if route.ConnectTimeout > 0 {
		transport.TLSHandshakeTimeout = route.ConnectTimeout
	} else {
		transport.TLSHandshakeTimeout = 0
	}
	return &http.Client{Transport: transport}
}

type profileConfiguration struct {
	Provider  string `json:"model_provider"`
	OpenAI    string `json:"openai_base_url"`
	Catalog   string `json:"model_catalog_json"`
	Providers map[string]struct {
		Base       string `json:"base_url"`
		Catalog    string `json:"model_catalog_url"`
		AWS        any    `json:"aws"`
		OAuth      any    `json:"gateway_oauth"`
		WebSockets bool   `json:"supports_websockets"`
		Connect    int64  `json:"websocket_connect_timeout_ms"`
		Idle       int64  `json:"stream_idle_timeout_ms"`
	} `json:"model_providers"`
}

// Resolve inherited provider fields through Codex before constructing transport
// owners. Only provider identity and the inference endpoint must be explicit;
// catalog, authentication, capabilities and timeouts retain normal host merging.
func resolveProfileRoute(ctx context.Context, executable string, args, environment []string, workspace string, route *inferencegateway.NativeProvider, standalone bool) ([]string, error) {
	command := exec.CommandContext(ctx, executable, args...)
	command.Env, command.Dir = environment, workspace
	configuration, err := profileConfigurationProbe(ctx, command, standalone)
	if err != nil {
		return nil, err
	}
	var config profileConfiguration
	if err := json.Unmarshal(configuration, &config); err != nil {
		return nil, errors.New("cannot resolve selected provider configuration")
	}
	provider := config.Providers[route.ID]
	base := provider.Base
	if route.ID == "openai" {
		base = config.OpenAI
	}
	if config.Provider != route.ID || base != route.BaseURL || provider.AWS != nil || provider.OAuth != nil {
		return nil, errors.New("effective provider settings or managed policy prohibit inference interception")
	}
	if provider.Catalog != "" {
		if route.ID == "openai" {
			return nil, errors.New("built-in OpenAI model_catalog_url cannot be intercepted; use a custom Responses provider")
		}
		if err := validateProfileEndpoint(provider.Catalog); err != nil {
			return nil, errors.New("invalid provider model_catalog_url")
		}
	}
	route.CatalogURL = provider.Catalog
	route.WebSockets = provider.WebSockets
	route.ConnectTimeout, route.StreamIdleTimeout = profileMilliseconds(provider.Connect), profileMilliseconds(provider.Idle)
	// Freeze the host-merged provider definition as private invocation input.
	// Children must not reload changed base/project credentials or capabilities.
	var raw struct {
		Providers map[string]map[string]jsontext.Value `json:"model_providers"`
	}
	if err := json.Unmarshal(configuration, &raw); err != nil {
		return nil, errors.New("cannot retain selected provider configuration")
	}
	definition := make(map[string]any)
	for key, encoded := range raw.Providers[route.ID] {
		if string(encoded) == "null" {
			continue
		}
		var value any
		if err := json.Unmarshal(encoded, &value, json.WithUnmarshalers(json.UnmarshalFromFunc(func(dec *jsontext.Decoder, target *any) error {
			if dec.PeekKind() != '0' {
				return errors.ErrUnsupported
			}
			var number int64
			if err := json.UnmarshalDecode(dec, &number); err != nil {
				return err
			}
			*target = number
			return nil
		}))); err != nil {
			return nil, errors.New("cannot retain selected provider field")
		}
		definition[key] = value
	}
	route.HeaderNames = profileHeaderNames(definition)
	providers, _ := launchoptions.ConfigOverrides(args)["model_providers"].(map[string]any)
	if providers == nil {
		providers = make(map[string]any)
	}
	providers[route.ID] = definition
	encoded, err := launchoptions.TOMLValue(providers)
	if err != nil {
		return nil, errors.New("cannot retain selected provider definition")
	}
	args = appendInvocationOverrides(args, "-c", "model_providers="+encoded)
	if config.Catalog != "" {
		args = appendInvocationOverrides(args, "-c", fmt.Sprintf("model_catalog_json=%q", config.Catalog))
	}
	return args, nil
}

// verifyProfileRoute uses Codex's effective configuration, including managed
// policy and base/project merging. A managed endpoint must not bypass capture.
func verifyProfileRoute(configuration []byte, route *inferencegateway.NativeProvider, local string) error {
	var config profileConfiguration
	if err := json.Unmarshal(configuration, &config); err != nil {
		return errors.New("cannot verify selected provider configuration")
	}
	provider := config.Providers[route.ID]
	base := provider.Base
	if route.ID == "openai" {
		base = config.OpenAI
	}
	if config.Provider != route.ID || base != local || provider.AWS != nil || provider.OAuth != nil || route.CatalogURL != "" && provider.Catalog != local+"/models" || route.CatalogURL == "" && provider.Catalog != "" {
		return errors.New("effective provider settings or managed policy prohibit inference/catalog interception")
	}
	return nil
}

// Wrapped exec uses the same non-generating configuration probe as native
// startup, with only its shared configuration flags rather than exec operands.
func profileConfigurationProbe(ctx context.Context, command *exec.Cmd, standalone bool) ([]byte, error) {
	var args []string
	for i := 1; i < len(command.Args) && command.Args[i] != "--"; i++ {
		if value, ok := codexArgumentValue(command.Args, &i, "-c", "--config"); ok {
			args = append(args, "-c", value)
		} else if value, ok := codexArgumentValue(command.Args, &i, "-m", "--model"); ok {
			args = append(args, "-c", fmt.Sprintf("model=%q", value))
		} else if command.Args[i] == "--enable" || command.Args[i] == "--disable" {
			if i+1 < len(command.Args) {
				args = append(args, command.Args[i], command.Args[i+1])
				i++
			}
		} else if flag, _, ok := strings.Cut(command.Args[i], "="); ok && (flag == "--enable" || flag == "--disable") {
			args = append(args, command.Args[i])
		} else if launchoptions.CodexOptionOperand(command.Args[i]) {
			i++
		}
	}
	if standalone {
		args = nativeAppServerArgs(args)
	} else {
		args = append([]string{"app-server"}, args...)
	}
	probe := exec.CommandContext(ctx, command.Path, args...)
	probe.Env, probe.Dir = command.Env, command.Dir
	_, _, configuration, err := shellhook.Discover(ctx, probe, false)
	if err != nil {
		// Host diagnostics can contain expanded profile values.
		return nil, errors.New("cannot resolve profile host configuration")
	}
	return configuration, nil
}

func validateProfileIntegration(args []string) error {
	values := launchoptions.ConfigOverrides(args)
	features, _ := values["features"].(map[string]any)
	for key, expected := range map[string]bool{"code_mode": true, "code_mode_only": true, "sleep_tool": false, "goals": false, "retain_client_tool_outputs": false} {
		if value, exists := features[key]; exists && value != expected {
			return fmt.Errorf("profile setting features.%s conflicts with mandatory execution integration", key)
		}
	}
	for feature, required := range map[string]map[string]any{
		"token_budget":   {"enabled": true, "use_history_notes_extension": false, "guidance_message": "", "auto_compact_fallback_prompt": "", "reminder_message_template": "Context remaining: {n_remaining} tokens."},
		"multi_agent_v2": {"enabled": true, "disable_native_tools": true},
	} {
		if value, exists := features[feature]; exists {
			if enabled, ok := value.(bool); ok {
				if !enabled {
					return fmt.Errorf("profile setting features.%s conflicts with mandatory execution integration", feature)
				}
				continue
			}
			fields, _ := value.(map[string]any)
			for key, expected := range required {
				if value, exists := fields[key]; exists && value != expected {
					return fmt.Errorf("profile setting features.%s.%s conflicts with mandatory execution integration", feature, key)
				}
			}
		}
	}
	if value, exists := values["include_collaboration_mode_instructions"]; exists && value != false {
		return errors.New("profile setting include_collaboration_mode_instructions conflicts with mandatory execution integration")
	}
	for i := 0; i < len(args) && args[i] != "--"; i++ {
		arg := args[i]
		if arg == "--enable" || arg == "--disable" || strings.HasPrefix(arg, "--enable=") || strings.HasPrefix(arg, "--disable=") {
			flag, feature, inline := strings.Cut(arg, "=")
			if !inline && i+1 < len(args) {
				i++
				feature = args[i]
			}
			if ((feature == "code_mode" || feature == "code_mode_only" || feature == "token_budget") && flag == "--disable") || ((feature == "goals" || feature == "sleep_tool" || feature == "retain_client_tool_outputs") && flag == "--enable") {
				return fmt.Errorf("profile feature %s conflicts with mandatory execution integration", feature)
			}
		} else if launchoptions.CodexOptionOperand(arg) {
			i++
		}
	}
	return nil
}

// Context's private environment is admitted over the native control channel;
// children borrow threadSetup instead. Never mutate the daemon environment.
type invocationEnvironmentKey struct{}

func invocationEnvironment(ctx context.Context) []string {
	if environment, ok := ctx.Value(invocationEnvironmentKey{}).([]string); ok {
		return slices.Clone(environment)
	}
	return os.Environ()
}

func invocationExecutable(name string, environment []string, workspace string) (string, error) {
	directories := filepath.SplitList(launchoptions.EnvironmentValue(environment, "PATH"))
	if name == "mekugi-app-server" {
		if executable, err := os.Executable(); err == nil {
			directories = append(directories, filepath.Dir(executable))
		}
	}
	for _, directory := range directories {
		if !filepath.IsAbs(directory) {
			directory = filepath.Join(workspace, directory)
		}
		path, err := filepath.Abs(filepath.Join(directory, name))
		if err != nil {
			continue
		}
		if info, err := os.Stat(path); err == nil && !info.IsDir() && info.Mode()&0111 != 0 {
			return path, nil
		}
	}
	return "", exec.ErrNotFound
}

func headlessCommand(args []string) ([]string, bool) {
	for i := 0; i < len(args); i++ {
		if args[i] == "--" {
			break
		}
		if args[i] == "headless" {
			return slices.Delete(slices.Clone(args), i, i+1), true
		}
		if args[i] == "-p" || args[i] == "--profile" || launchoptions.CodexOptionOperand(args[i]) {
			i++
		} else if !strings.HasPrefix(args[i], "-") {
			break
		}
	}
	return nil, false
}

func sessionInvocation(args []string) bool {
	if interactiveCodexArgs(args) {
		return true
	}
	for i := 0; i < len(args); i++ {
		if args[i] == "--" {
			return true
		}
		if args[i] == "-p" || args[i] == "--profile" || launchoptions.CodexOptionOperand(args[i]) {
			i++
		} else if !strings.HasPrefix(args[i], "-") {
			return args[i] == "exec" || args[i] == "e" || args[i] == "review" || args[i] == "headless"
		}
	}
	return true
}

func profileResumeConfig(args, resumeArgv []string, profile *launchoptions.Profile) map[string]any {
	config := appServerResumeConfig(args, resumeArgv)
	values := launchoptions.ConfigOverrides(args)
	for _, key := range profile.Keys {
		if value, ok := values[key]; ok {
			config[key] = value
		}
	}
	if len(resumeArgv) > 1 {
		_, explicit, err := launchoptions.SplitCommand(resumeArgv[1:])
		if err == nil {
			for key := range launchoptions.ConfigOverrides(explicit) {
				if value, ok := values[key]; ok {
					config[key] = value
				}
			}
		}
	}
	return config
}

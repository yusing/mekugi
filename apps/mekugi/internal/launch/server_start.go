// Source: apps/mekugi/internal/launch/web_start.go@14a6d1f3 catalog and environment policy.
package launch

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/yusing/mekugi/apps/mekugi/internal/frontends"
	"github.com/yusing/mekugi/apps/mekugi/internal/headless"
	"github.com/yusing/mekugi/apps/mekugi/internal/launch/modelcatalog"
	launchoptions "github.com/yusing/mekugi/apps/mekugi/internal/launch/options"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/backends/codex/rollout"
	threadcontrol "github.com/yusing/mekugi/modules/threadcontrol/control"
)

type serverLauncher struct {
	headless *headless.Stream
	workers  frontends.WorkerImage
	threads  *threadcontrol.Service
	hub      *sessionapi.Hub
	mu       sync.Mutex
	selected *modelcatalog.Selection
	catalog  *sessionapi.StartOptions
}

func (l *serverLauncher) PrepareInput(ctx context.Context, workspace string, input sessionapi.InputRequest) (*sessionapi.PreparedInput, error) {
	return prepareAPIInput(ctx, workspace, input)
}

func (l *serverLauncher) StartOptions(ctx context.Context) (sessionapi.StartOptions, error) {
	options, err := l.models(ctx)
	if err != nil {
		return options, err
	}
	options.Workspaces = recentServerWorkspaces(ctx)
	return options, nil
}

func (l *serverLauncher) models(ctx context.Context) (sessionapi.StartOptions, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.catalog != nil {
		return *l.catalog, nil
	}
	if l.selected == nil {
		settings, err := launchoptions.LoadSettings(true)
		if err != nil {
			return sessionapi.StartOptions{}, err
		}
		auth, providers, err := launchProviders(ctx, launchoptions.Config{ThirdParty: true}, settings.Providers)
		if err != nil {
			return sessionapi.StartOptions{}, err
		}
		l.selected = &modelcatalog.Selection{GrokEnabled: auth != nil, OpenCode: providers}
	}
	options := defaultStartOptions(*l.selected)
	executable, err := exec.LookPath("codex")
	var body []byte
	if err == nil {
		body, err = bundledModelCatalog(ctx, executable, "")
	}
	if err == nil {
		options, err = startModelOptions(body, *l.selected)
	}
	if err != nil {
		if ctx.Err() != nil {
			return options, ctx.Err()
		}
		options = defaultStartOptions(*l.selected)
		options.Note = "The full model catalog is unavailable; provider defaults are still available."
		return options, nil
	}
	l.catalog = &options
	return options, nil
}

func defaultStartOptions(selection modelcatalog.Selection) sessionapi.StartOptions {
	options := sessionapi.StartOptions{DefaultModel: "codex", Models: []sessionapi.ModelGroup{{Label: "Codex", Models: []sessionapi.Model{{ID: "codex", Label: "Default"}}}}}
	if selection.GrokEnabled || selection.OpenCode.Enabled() {
		options.DefaultModel = "mekugi"
		if selection.GrokEnabled {
			options.Models = append(options.Models, sessionapi.ModelGroup{Label: "Grok", Models: []sessionapi.Model{{ID: "mekugi", Label: "Mekugi default"}}})
		}
		for _, service := range selection.OpenCode.Services() {
			options.Models = append(options.Models, sessionapi.ModelGroup{Label: service.Label, Models: []sessionapi.Model{{ID: "mekugi", Label: "Mekugi default"}}})
		}
	}
	return options
}

func startModelOptions(body []byte, selection modelcatalog.Selection) (sessionapi.StartOptions, error) {
	options := defaultStartOptions(selection)
	type catalogModel struct {
		Slug       string `json:"slug"`
		Label      string `json:"display_name"`
		Visibility string `json:"visibility"`
		Default    string `json:"default_reasoning_level"`
		Efforts    []struct {
			Effort string `json:"effort"`
		} `json:"supported_reasoning_levels"`
	}
	read := func(body []byte, namespace string) error {
		var catalog struct {
			Models []catalogModel `json:"models"`
		}
		if err := json.Unmarshal(body, &catalog); err != nil {
			return err
		}
		if len(catalog.Models) == 0 {
			return errors.New("model catalog has no models")
		}
		for _, entry := range catalog.Models {
			if entry.Visibility != "list" || entry.Slug == "" {
				continue
			}
			group := 0
			if namespace == "mekugi" {
				label := ""
				switch {
				case strings.HasPrefix(entry.Slug, "grok:"):
					label = "Grok"
				case strings.HasPrefix(entry.Slug, "opencode-go:"):
					label = "OpenCode Go"
				case strings.HasPrefix(entry.Slug, "opencode-zen:"):
					label = "OpenCode Zen"
				}
				group = slices.IndexFunc(options.Models, func(g sessionapi.ModelGroup) bool { return g.Label == label })
				if group < 0 {
					continue
				}
			}
			model := sessionapi.Model{ID: namespace + "/" + entry.Slug, Label: entry.Label, DefaultEffort: entry.Default}
			if model.Label == "" {
				model.Label = entry.Slug
			}
			for _, effort := range entry.Efforts {
				model.Efforts = append(model.Efforts, effort.Effort)
			}
			options.Models[group].Models = append(options.Models[group].Models, model)
		}
		return nil
	}
	if err := read(body, "codex"); err != nil {
		return options, err
	}
	if selection.GrokEnabled || selection.OpenCode.Enabled() {
		thirdParty, err := modelcatalog.Build(body, selection)
		if err != nil {
			return options, err
		}
		if err := read(thirdParty, "mekugi"); err != nil {
			return options, err
		}
	}
	return options, nil
}

func (l *serverLauncher) Run(ctx context.Context, start sessionapi.Start, api *sessionapi.Server) error {
	if start.Replay != nil {
		return runServerReplay(ctx, *start.Replay, api)
	}
	var execution *threadExecution
	if l.hub != nil {
		execution = &threadExecution{hub: l.hub, shared: l.threads, workers: &l.workers, headless: l.headless}
	}
	if start.Native != nil {
		code, err := runServerSession(ctx, start.Native.RouterArgs, start.Native.Args, api, start.Workspace, execution)
		if l.headless != nil {
			if err == nil && code != 0 {
				err = fmt.Errorf("session exited with status %d", code)
			}
			return err
		}
		outcome := &sessionapi.NativeExit{Code: code}
		if err != nil {
			outcome.Message = err.Error()
		} else if code != 0 {
			outcome.Message = fmt.Sprintf("session exited with status %d", code)
		}
		return outcome
	}
	var routerArgs []string
	namespace, model, _ := strings.Cut(start.Model, "/")
	if namespace != "codex" {
		routerArgs = []string{"third-party"}
	}
	var args []string
	if model != "" {
		args = append(args, "-m", model)
	}
	if start.Effort != "" {
		args = append(args, "-c", fmt.Sprintf("model_reasoning_effort=%q", start.Effort))
	}
	if start.Resume != "" {
		args = append(args, "resume", start.Resume)
	}
	code, err := runServerSession(ctx, routerArgs, args, api, start.Workspace, execution)
	if err == nil && code != 0 {
		return fmt.Errorf("session exited with status %d", code)
	}
	return err
}

func serverEnvironment(environment []string) []string {
	values := map[string]string{}
	for _, entry := range environment {
		key, value, _ := strings.Cut(entry, "=")
		values[key] = value
	}
	frontendDirectory := ""
	if path := values["BASH_ENV"]; filepath.Base(path) == "frontend-bash-env" {
		if body, err := os.ReadFile(path); err == nil && strings.HasPrefix(string(body), frontendBashEnvMarker) {
			frontendDirectory = filepath.Dir(path)
		}
	}
	result := make([]string, 0, len(environment))
	for _, entry := range environment {
		key, value, _ := strings.Cut(entry, "=")
		if slices.Contains([]string{"CODEX_THREAD_ID", "MEKUGI_BASE_URL", "MEKUGI_RUNTIME_DIR", userBashEnvEnvironment, "MEKUGI_EXEC_TRACK", "MEKUGI_SH_TRACKER", "MEKUGI_SUDO_DIRECTORY", "MEKUGI_VCS_ITEM", "MEKUGI_VCS_GUARD_REAL", "TMUX", "TMUX_PANE", "TERM"}, key) || strings.HasPrefix(key, "HERDR_") {
			continue
		}
		if key == "BASH_ENV" && frontendDirectory != "" {
			if previous := values[userBashEnvEnvironment]; previous != "" {
				result = append(result, "BASH_ENV="+previous)
			}
			continue
		}
		if key == "PATH" && frontendDirectory != "" {
			paths := filepath.SplitList(value)
			paths = slices.DeleteFunc(paths, func(path string) bool {
				relative, err := filepath.Rel(frontendDirectory, path)
				return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
			})
			entry = "PATH=" + strings.Join(paths, string(os.PathListSeparator))
		}
		result = append(result, entry)
	}
	terminal := values["TERM"]
	if terminal == "" || terminal == "dumb" {
		terminal = "xterm-256color"
	}
	return append(result, "TERM="+terminal)
}

func recentServerWorkspaces(ctx context.Context) []string {
	directory, err := rollout.HomeDirectory()
	if err != nil {
		return nil
	}
	var workspaces []string
	files, directories := 0, 0
	var walk func(string, int)
	walk = func(directory string, depth int) {
		if ctx.Err() != nil || files >= 400 || len(workspaces) >= 20 || directories >= 1600 {
			return
		}
		directories++
		entries, err := os.ReadDir(directory)
		if err != nil {
			return
		}
		for _, entry := range slices.Backward(entries) {
			if ctx.Err() != nil || files >= 400 || len(workspaces) >= 20 {
				return
			}

			path := filepath.Join(directory, entry.Name())
			if entry.IsDir() && depth < 3 {
				walk(path, depth+1)
				continue
			}
			if depth != 3 || !strings.HasSuffix(entry.Name(), ".jsonl") {
				continue
			}
			files++
			header, err := rollout.ReadHeader(path)
			if err != nil || !filepath.IsAbs(header.Workspace) || slices.Contains(workspaces, header.Workspace) {
				continue
			}
			if info, err := os.Stat(header.Workspace); err == nil && info.IsDir() {
				workspaces = append(workspaces, header.Workspace)
			}
		}
	}
	walk(filepath.Join(directory, "sessions"), 0)
	return workspaces
}

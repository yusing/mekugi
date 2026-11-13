package query

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/libraries/sourceanalysis/sourceparse"
	"github.com/yusing/mekugi/libraries/sourceanalysis/structure"
)

// Cargo and rustup must fail on missing prerequisites, never fetch or install.
var rustEnvironment = []string{"CARGO_NET_OFFLINE=true", "RUSTUP_AUTO_INSTALL=0"}

func rustCoverage(message string) error {
	return &symbolFailure{"incomplete_coverage", "Rust coverage incomplete: " + message}
}

// A batch owns one server, but each Cargo project keeps its native configuration
// and installed toolchain. Source inventories are shared by queries in that project.
type rustAnalysis struct {
	config  map[string]any
	sources map[*symbolQuery][]string
	rustup  string
}

func prepareRust(ctx context.Context, scope command.ProcessScope, root, scratch string, queries []*symbolQuery) (*rustAnalysis, error) {
	run := func(directory, name string, args ...string) ([]byte, error) {
		cmd := resolverCommand(ctx, scope, directory, name, args...)
		cmd.Env = append(cmd.Environ(), rustEnvironment...)
		if len(args) > 0 && args[0] == "-Z" {
			cmd.Env = append(cmd.Env, "RUSTC_BOOTSTRAP=1")
		}
		cmd.Env = append(cmd.Env, "TMPDIR="+scratch, "TMP="+scratch, "TEMP="+scratch)
		out := &boundedResolverOutput{limit: retainedBytes + 1}
		diagnostic := &boundedResolverOutput{limit: 8192}
		cmd.Stdout, cmd.Stderr = out, diagnostic
		err := cmd.Run()
		retireResolver(cmd, scope)
		if ctx.Err() != nil {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return nil, resolverTimeoutError()
			}
			return nil, ctx.Err()
		}
		if err != nil {
			return nil, fmt.Errorf("Rust prerequisite %s: %w: %s", name, resolverStartError(name, err), diagnostic.String())
		}
		if out.Len() > retainedBytes {
			return nil, rustCoverage("prerequisite output exceeds 16 MiB")
		}
		return out.Bytes(), nil
	}
	manifests := map[string]string{}
	queryManifests := map[*symbolQuery]string{}
	for _, q := range queries {
		for dir := filepath.Dir(q.file.path); !outsideSymbolWorkspace(root, dir); dir = filepath.Dir(dir) {
			candidate := filepath.Join(dir, "Cargo.toml")
			if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
				canonical, err := filepath.EvalSymlinks(candidate)
				if err != nil || outsideSymbolWorkspace(root, canonical) {
					return nil, rustCoverage("Cargo manifest escapes workspace")
				}
				manifests[canonical] = ""
				queryManifests[q] = canonical
				break
			}
			if dir == root {
				break
			}
		}
		if queryManifests[q] == "" {
			q.err = rustCoverage("standalone Rust files and non-Cargo projects are unsupported")
		}
	}
	if len(manifests) == 0 {
		return nil, rustCoverage("no Cargo project inside selected workspace")
	}
	roots := map[string]bool{}
	for manifest := range manifests {
		located, err := run(filepath.Dir(manifest), "cargo", "locate-project", "--workspace", "--manifest-path", manifest, "--message-format", "plain")
		if err != nil {
			return nil, err
		}
		canonical, err := filepath.EvalSymlinks(strings.TrimSpace(string(located)))
		if err != nil || outsideSymbolWorkspace(root, canonical) {
			return nil, rustCoverage("Cargo workspace manifest escapes selected workspace")
		}
		manifests[manifest] = canonical
		roots[canonical] = true
	}
	if _, err := run(root, "rust-analyzer", "--version"); err != nil {
		return nil, err
	}
	// rust-analyzer otherwise invokes rustup component add when rust-src is absent.
	// Its supported RUSTUP executable override blocks that explicit installer too;
	// RUSTUP_AUTO_INSTALL=0 separately blocks installations through rustup proxies.
	rustup := filepath.Join(scratch, "rustup-no-install")
	if err := os.WriteFile(rustup, []byte("#!/bin/sh\nprintf '%s\\n' 'msymbol: automatic Rust component/toolchain installation is disabled' >&2\nexit 1\n"), 0700); err != nil {
		return nil, err
	}
	configDirs := map[string]bool{}
	sourceRoots := map[string]bool{}
	graphRoots := map[string]map[string]bool{}
	exclusions := map[string]bool{}
	coverage := map[string]error{}
	linked := make([]string, 0, len(roots))
	for manifest := range roots {
		linked = append(linked, manifest)
	}
	slices.Sort(linked)
	for _, manifest := range linked {
		directory := filepath.Dir(manifest)
		sysrootData, err := run(directory, "rustc", "--print", "sysroot")
		if err != nil {
			return nil, err
		}
		src := filepath.Join(strings.TrimSpace(string(sysrootData)), "lib", "rustlib", "src", "rust", "library", "core", "src", "lib.rs")
		if info, err := os.Stat(src); err != nil || !info.Mode().IsRegular() {
			return nil, &symbolFailure{"dependency_unavailable", "Rust standard-library sources unavailable in selected toolchain; prepare rust-src separately (nothing installed)"}
		}
		configRaw, err := run(directory, "cargo", "-Z", "unstable-options", "config", "get", "--format", "json")
		if err != nil {
			return nil, err
		}
		var cargoConfig struct {
			Build struct {
				Target any `json:"target"`
			} `json:"build"`
		}
		if err := json.Unmarshal(configRaw, &cargoConfig); err != nil {
			return nil, fmt.Errorf("Rust Cargo configuration: %w", err)
		}
		var platforms []string
		switch target := cargoConfig.Build.Target.(type) {
		case string:
			platforms = append(platforms, target)
		case []any:
			for _, p := range target {
				value, ok := p.(string)
				if !ok {
					return nil, rustCoverage("invalid Cargo build.target")
				}
				platforms = append(platforms, value)
			}
		case nil:
		default:
			return nil, rustCoverage("invalid Cargo build.target")
		}
		if len(platforms) == 0 {
			version, err := run(directory, "rustc", "-vV")
			if err != nil {
				return nil, err
			}
			for line := range strings.Lines(string(version)) {
				if host, ok := strings.CutPrefix(strings.TrimSpace(line), "host: "); ok {
					platforms = append(platforms, host)
				}
			}
			if len(platforms) == 0 {
				return nil, rustCoverage("rustc did not identify the selected host target")
			}
		}
		// Locked/offline metadata cannot update even a missing source-tree lockfile.
		args := []string{"metadata", "--offline", "--locked", "--format-version", "1", "--manifest-path", manifest}
		for _, platform := range platforms {
			args = append(args, "--filter-platform", platform)
		}
		raw, err := run(directory, "cargo", args...)
		if err != nil {
			return nil, err
		}
		var metadata struct {
			WorkspaceRoot    string   `json:"workspace_root"`
			WorkspaceMembers []string `json:"workspace_members"`
			TargetDirectory  string   `json:"target_directory"`
			Resolve          struct {
				Nodes []struct {
					ID   string `json:"id"`
					Deps []struct {
						Pkg string `json:"pkg"`
					} `json:"deps"`
				} `json:"nodes"`
			} `json:"resolve"`
			Packages []struct {
				ID           string  `json:"id"`
				ManifestPath string  `json:"manifest_path"`
				Source       *string `json:"source"`
				Targets      []struct {
					Kind    []string `json:"kind"`
					SrcPath string   `json:"src_path"`
				} `json:"targets"`
			} `json:"packages"`
		}
		if err := json.Unmarshal(raw, &metadata); err != nil {
			return nil, fmt.Errorf("Rust Cargo metadata: %w", err)
		}
		canonical, err := filepath.EvalSymlinks(metadata.WorkspaceRoot)
		if err != nil || outsideSymbolWorkspace(root, canonical) {
			return nil, rustCoverage("Cargo workspace root escapes selected workspace")
		}
		if len(metadata.WorkspaceMembers) == 0 || len(metadata.Resolve.Nodes) == 0 {
			return nil, rustCoverage("Cargo metadata has no resolved workspace graph")
		}
		configDirs[canonical] = true
		if metadata.TargetDirectory != "" {
			exclusions[metadata.TargetDirectory] = true
		}
		active := map[string]bool{}
		edges := map[string][]string{}
		for _, node := range metadata.Resolve.Nodes {
			for _, dep := range node.Deps {
				edges[node.ID] = append(edges[node.ID], dep.Pkg)
			}
		}
		var visit func(string)
		visit = func(id string) {
			if active[id] {
				return
			}
			active[id] = true
			for _, dep := range edges[id] {
				visit(dep)
			}
		}
		for _, id := range metadata.WorkspaceMembers {
			visit(id)
		}
		graphRoots[manifest] = map[string]bool{}
		for _, pkg := range metadata.Packages {
			if pkg.Source == nil {
				pkgDir := filepath.Dir(pkg.ManifestPath)
				configDirs[pkgDir] = true
				sourceRoots[pkgDir] = sourceRoots[pkgDir] || active[pkg.ID]
				graphRoots[manifest][pkgDir] = active[pkg.ID]
				exclusions[filepath.Join(pkgDir, ".git")] = true
				exclusions[filepath.Join(pkgDir, "target")] = true
				if !slices.Contains(metadata.WorkspaceMembers, pkg.ID) {
					for _, name := range []string{"tests", "examples", "benches"} {
						exclusions[filepath.Join(pkgDir, name)] = true
					}
				}
				for _, target := range pkg.Targets {
					if target.SrcPath != "" {
						dir := filepath.Dir(target.SrcPath)
						if outsideSymbolWorkspace(pkgDir, dir) {
							sourceRoots[dir] = sourceRoots[dir] || active[pkg.ID]
							graphRoots[manifest][dir] = active[pkg.ID]
						}
					}
				}
			}
			for _, target := range pkg.Targets {
				if active[pkg.ID] && (slices.Contains(target.Kind, "custom-build") || slices.Contains(target.Kind, "proc-macro")) {
					coverage[manifest] = rustCoverage("selected Cargo graph contains disabled build scripts or procedural macros; generated references are unavailable")
				}
			}
		}
	}
	config := map[string]any{
		"cachePriming":   map[string]any{"enable": false},
		"linkedProjects": linked,
		"cargo": map[string]any{
			"buildScripts": map[string]any{"enable": false, "rebuildOnSave": false, "overrideCommand": nil},
			"extraArgs":    []string{"--offline", "--locked"}, "metadataExtraArgs": []string{"--offline", "--locked"},
			"extraEnv": map[string]string{"CARGO_NET_OFFLINE": "true", "RUSTUP_AUTO_INSTALL": "0", "RUSTUP": rustup},
			"sysroot":  "discover", "sysrootSrc": nil, "features": []string{}, "noDefaultFeatures": false, "autoreload": true, "configPath": nil, "noDeps": false, "target": nil, "cfgs": []string{},
		},
		"procMacro": map[string]any{"enable": false, "server": nil}, "checkOnSave": false, "check": map[string]any{"overrideCommand": nil},
		"workspace": map[string]any{"discoverConfig": nil}, "rustc": map[string]any{"source": nil}, "files": map[string]any{"watcher": "client", "exclude": []string{}},
		"diagnostics": map[string]any{"enable": true, "disabled": []string{}},
	}
	for dir := range configDirs {
		for _, name := range []string{"rust-analyzer.toml", ".rust-analyzer.toml"} {
			if err := checkRustConfigFile(filepath.Join(dir, name), config); err != nil {
				return nil, err
			}
		}
	}
	sources, err := scanRustSources(ctx, sourceRoots, exclusions, config)
	if err != nil {
		return nil, err
	}
	projectSources := map[string][]string{}
	for manifest, dirs := range graphRoots {
		seen := map[string]bool{}
		for dir, active := range dirs {
			if !active {
				continue
			}
			for _, path := range sources[dir] {
				if !seen[path] {
					seen[path] = true
					projectSources[manifest] = append(projectSources[manifest], path)
				}
			}
		}
		slices.Sort(projectSources[manifest])
	}
	analysis := &rustAnalysis{config: config, sources: map[*symbolQuery][]string{}, rustup: rustup}
	for _, q := range queries {
		manifest := manifests[queryManifests[q]]
		q.coverage = coverage[manifest]
		analysis.sources[q] = projectSources[manifest]
	}
	return analysis, nil
}

// Match the server's local discovery, retaining traversal membership across
// symlinks. Canonical containment alone loses linked modules outside a package.
func scanRustSources(ctx context.Context, roots map[string]bool, exclusions map[string]bool, config map[string]any) (map[string][]string, error) {
	directories := map[string][]os.DirEntry{}
	checkedConfig := map[string]bool{}
	inventories := map[string][]string{}
	for root, active := range roots {
		visited := map[string]bool{}
		sources := map[string]string{}
		var walk func(string) error
		walk = func(path string) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if exclusions[path] {
				return nil
			}
			canonical, err := filepath.EvalSymlinks(path)
			if os.IsNotExist(err) {
				return nil
			}
			if err != nil {
				return err
			}
			if exclusions[canonical] {
				return nil
			}
			info, err := os.Stat(canonical)
			if err != nil {
				return err
			}
			if info.IsDir() {
				if visited[canonical] {
					return nil
				}
				visited[canonical] = true
				entries, ok := directories[canonical]
				if !ok {
					entries, err = os.ReadDir(canonical)
					if err != nil {
						return err
					}
					directories[canonical] = entries
				}
				for _, entry := range entries {
					if err := walk(filepath.Join(path, entry.Name())); err != nil {
						return err
					}
				}
				return nil
			}
			name := filepath.Base(path)
			if (name == "rust-analyzer.toml" || name == ".rust-analyzer.toml") && !checkedConfig[canonical] {
				if err := checkRustConfigFile(path, config); err != nil {
					return err
				}
				checkedConfig[canonical] = true
			}
			if active && info.Mode().IsRegular() && strings.HasSuffix(name, ".rs") {
				if _, ok := sources[canonical]; !ok {
					sources[canonical] = path
				}
			}
			return nil
		}
		if err := walk(root); err != nil {
			return nil, err
		}
		for _, path := range sources {
			inventories[root] = append(inventories[root], path)
		}
		slices.Sort(inventories[root])
	}
	return inventories, nil
}

func checkRustConfigFile(path string, config map[string]any) error {
	file, err := sourceparse.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return rustCoverage("configuration is not a regular file: " + path)
	}
	text, err := sourceparse.ReadText(file, retainedBytes, "Rust configuration must be UTF-8 and at most 16 MiB")
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	var settings map[string]any
	if err := toml.Unmarshal([]byte(text), &settings); err != nil {
		return fmt.Errorf("Rust configuration %s: %w", path, err)
	}
	if err := checkRustConfiguration(settings, config, ""); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// Workspace TOML outranks LSP settings. Allow harmless editor preferences but
// reject overrides of semantic admission, coverage, or execution policy.
func checkRustConfiguration(settings, safe map[string]any, prefix string) error {
	for key, value := range settings {
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		expected, protected := safe[key]
		if prefix == "" && !slices.Contains([]string{"cargo", "procMacro", "checkOnSave", "check", "workspace", "rustc", "linkedProjects", "files", "diagnostics", "cfg"}, key) {
			continue
		}
		if child, ok := value.(map[string]any); ok {
			target, _ := expected.(map[string]any)
			if err := checkRustConfiguration(child, target, path); err != nil {
				return err
			}
			continue
		}
		// TOML arrays decode as []any; compare JSON to the typed initialization values.
		actualJSON, _ := json.Marshal(value)
		expectedJSON, _ := json.Marshal(expected)
		if !protected || !bytes.Equal(actualJSON, expectedJSON) {
			return rustCoverage("project configuration overrides protected setting " + path)
		}
	}
	return nil
}

type rustServerStatus struct {
	Health    string `json:"health"`
	Quiescent bool   `json:"quiescent"`
	Message   string `json:"message"`
}

func (l *lspClient) rustReady(ctx context.Context) error {
	for l.rustStatus == nil || !l.rustStatus.Quiescent {
		select {
		case <-ctx.Done():
			if ctx.Err() == context.DeadlineExceeded {
				return resolverTimeoutError()
			}
			return ctx.Err()
		case event, ok := <-l.events:
			if !ok {
				return rustCoverage("language server exited before ready")
			}
			if event.err != nil {
				return event.err
			}
			if err := l.handleServerMessage(event.message); err != nil {
				return err
			}
		}
	}
	if l.rustStatus.Health != "ok" {
		return rustCoverage("rust-analyzer " + l.rustStatus.Health + ": " + l.rustStatus.Message)
	}
	return nil
}

type rustDiagnosticReport struct {
	Kind  string `json:"kind"`
	Items []struct {
		Range    symbolRange `json:"range"`
		Severity int         `json:"severity"`
		Code     any         `json:"code"`
		Message  string      `json:"message"`
	} `json:"items"`
	RelatedDocuments map[string]*rustDiagnosticReport `json:"relatedDocuments"`
}

func (l *lspClient) rustDiagnostics(ctx context.Context, uri string, selection *structure.Position) error {
	report, ok := l.rustReports[uri]
	if !ok {
		raw, err := l.request(ctx, "textDocument/diagnostic", map[string]any{"textDocument": map[string]string{"uri": uri}})
		if err != nil {
			return rustCoverage("native diagnostics unavailable: " + err.Error())
		}
		if err := json.Unmarshal(raw, &report); err != nil || report.Kind != "full" {
			return rustCoverage("invalid native diagnostic report")
		}
		if l.rustReports == nil {
			l.rustReports = map[string]rustDiagnosticReport{}
		}
		l.rustReports[uri] = report
	}
	var check func(rustDiagnosticReport, *structure.Position) error
	check = func(report rustDiagnosticReport, selected *structure.Position) error {
		if report.Kind != "full" {
			return rustCoverage("invalid related native diagnostic report")
		}
		// Files outside the selected module graph cannot generate references in it.
		if selected == nil {
			for _, item := range report.Items {
				if fmt.Sprint(item.Code) == "unlinked-file" {
					return nil
				}
			}
		}
		for _, item := range report.Items {
			code := fmt.Sprint(item.Code)
			inactive := false
			if code == "inactive-code" && selected != nil {
				start, end := item.Range.Start, item.Range.End
				afterStart := selected.Line > start.Line || selected.Line == start.Line && selected.Character >= start.Character
				beforeEnd := selected.Line < end.Line || selected.Line == end.Line && selected.Character < end.Character
				inactive = afterStart && beforeEnd
			}
			if item.Severity == 1 || inactive || selected != nil && code == "unlinked-file" || strings.Contains(code, "unresolved") {
				return rustCoverage(code + ": " + item.Message)
			}
		}
		for related, report := range report.RelatedDocuments {
			if report == nil {
				continue
			}
			if err := check(*report, nil); err != nil {
				return fmt.Errorf("%s: %w", related, err)
			}
		}
		return nil
	}
	return check(report, selection)
}

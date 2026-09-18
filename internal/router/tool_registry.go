package router

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/yusing/mekugi"
	"github.com/yusing/mekugi/internal/router/toolplugin"
	"github.com/yusing/mekugi/internal/shellruntime"
)

const (
	toolPluginManifestFilename = "workers.json"
	builtinToolsPluginID       = "builtin.shell"
	reportIssueToolName        = "report_issue"
	reportIssueToolDescription = `Free-form Markdown issue report for an observed mekugi-related tool interaction.`
)

func buildToolRegistry(ctx context.Context, dataDirectory string, diagnose bool) (*toolRegistry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	runtimeDirectory, err := shellruntime.Directory()
	if err != nil {
		return nil, fmt.Errorf("locate shell runtime directory: %w", err)
	}
	replayDirectory, err := defaultMekugiReplayDirectory()
	if err != nil {
		return nil, err
	}
	return buildToolRegistryAt(ctx, dataDirectory, diagnose, runtimeDirectory, replayDirectory)
}

func buildToolRegistryAt(
	ctx context.Context,
	dataDirectory string,
	diagnose bool,
	runtimeDirectory, replayDirectory string,
) (*toolRegistry, error) {
	if err := os.MkdirAll(runtimeDirectory, 0o700); err != nil {
		return nil, fmt.Errorf("create shell runtime directory: %w", err)
	}
	snapshotDirectory, err := os.MkdirTemp(runtimeDirectory, "mekugi-tools-")
	if err != nil {
		return nil, fmt.Errorf("create tool registry snapshot: %w", err)
	}
	wrappers := make(map[string]string)
	frontends := make(map[string]string)
	fail := func(cause error) (*toolRegistry, error) {
		return nil, errors.Join(
			cause,
			removeWorkerFrontendSymlinks(frontends, wrappers),
			os.RemoveAll(snapshotDirectory),
		)
	}
	if err := pinRunningToolWorker(snapshotDirectory); err != nil {
		return fail(err)
	}
	diagnoseHooks := mekugi.NewDiagnoseHooks("")
	if diagnose {
		diagnoseHooks = mekugi.NewDiagnoseHooks(dataDirectory)
	}

	pluginSnapshot, err := toolplugin.Load(
		ctx,
		filepath.Join(dataDirectory, "plugins"),
		filepath.Join(snapshotDirectory, "runtime"),
	)
	if err != nil {
		return fail(err)
	}
	contributions := []toolContribution{
		{PluginID: "builtin.mekugi", Name: "hread", Builtin: true},
		{PluginID: "builtin.mekugi", Name: "hchanges", Builtin: true},
		{PluginID: "builtin.mekugi", Name: mekugiToolName, Builtin: true},
	}
	if diagnose {
		contributions = append(contributions, toolContribution{
			PluginID:      "builtin.mekugi",
			Name:          reportIssueToolName,
			Specification: mustMarshalJSON(customFreeformTool(reportIssueToolName, reportIssueToolDescription)),
			Builtin:       true,
			ModelVisible:  true,
		})
	}
	var validationErrors []error
	for _, diagnostic := range pluginSnapshot.Diagnostics {
		validationErrors = append(validationErrors, errors.New(diagnostic))
	}
	pluginIDs := make(map[string]string)
	for _, plugin := range pluginSnapshot.Plugins {
		if prior, exists := pluginIDs[plugin.ID]; exists {
			validationErrors = append(validationErrors, fmt.Errorf(
				"plugin identity %q is declared by both %s and %s",
				plugin.ID,
				prior,
				plugin.Module,
			))
		} else {
			pluginIDs[plugin.ID] = plugin.Module
		}
		for toolIndex, tool := range plugin.Tools {
			specification, decodeErr := decodeResponsesToolDefinition(tool.Specification)
			if decodeErr != nil {
				validationErrors = append(validationErrors, fmt.Errorf(
					"%s tool %d: decode normalized specification: %w",
					plugin.Module,
					toolIndex+1,
					decodeErr,
				))
				continue
			}
			name := specification.Name
			contribution := toolContribution{
				PluginID:      plugin.ID,
				Name:          name,
				Specification: slices.Clone(tool.Specification),
				Module:        plugin.Module,
				ModuleIndex:   toolIndex,
				ModelVisible:  name != "hcat" && name != "hgrep" && name != "hsymbol" && name != "inspect_file",
			}
			if validationErr := validateToolContribution(contribution); validationErr != nil {
				validationErrors = append(validationErrors, validationErr)
			}
			contributions = append(contributions, contribution)
		}
	}
	byName := make(map[string]toolContribution, len(contributions))
	for _, contribution := range contributions {
		if prior, exists := byName[contribution.Name]; exists {
			validationErrors = append(validationErrors, fmt.Errorf(
				"tool name %q is owned by both %s and %s",
				contribution.Name,
				prior.PluginID,
				contribution.PluginID,
			))
			continue
		}
		byName[contribution.Name] = contribution
	}
	if len(validationErrors) != 0 {
		return fail(errors.Join(validationErrors...))
	}

	routing, err := toolplugin.LoadCommandRouting(ctx, pluginSnapshot.NodeExecutable, filepath.Join(snapshotDirectory, "runtime"))
	if err != nil {
		return fail(fmt.Errorf("load shell command routing policy: %w", err))
	}
	manifest := toolWorkerManifest{
		CommandRouting:  &routing,
		ReplayDirectory: replayDirectory,
		HookDirectory:   dataDirectory,
		Version:         1,
		NodeExecutable:  pluginSnapshot.NodeExecutable,
		RuntimeRoot:     "runtime",
		Tools:           slices.Clone(contributions),
	}
	if debug, _ := ctx.Value(debugContextKey{}).(*debugOutput); debug != nil {
		manifest.AXReadOutput = debug.paths[4]
	}
	registryID, err := toolRegistryIdentity(manifest, pluginSnapshot.Root)
	if err != nil {
		return fail(err)
	}
	manifest.RegistryID = registryID
	authenticatedDirectory := snapshotDirectory + "-" + registryID
	if err := os.Rename(snapshotDirectory, authenticatedDirectory); err != nil {
		return fail(fmt.Errorf("authenticate tool registry snapshot: %w", err))
	}
	snapshotDirectory = authenticatedDirectory
	executable := filepath.Join(snapshotDirectory, toolWorkerExecutableFilename)
	runtimeRoot := filepath.Join(snapshotDirectory, manifest.RuntimeRoot)
	if err := writeToolWorkerManifest(snapshotDirectory, manifest); err != nil {
		return fail(err)
	}
	var shellRuntime string
	for _, contribution := range contributions {
		if contribution.Builtin || (contribution.PluginID == builtinToolsPluginID && contribution.Name != "shell") {
			continue
		}
		wrapper, wrapperErr := ensureWorkerSymlinkInDirectory(executable, snapshotDirectory, contribution.Name)
		if wrapperErr != nil {
			validationErrors = append(validationErrors, wrapperErr)
			continue
		}
		if contribution.PluginID == builtinToolsPluginID {
			shellRuntime = wrapper
		} else {
			wrappers[contribution.Name] = wrapper
		}
	}
	if len(validationErrors) != 0 {
		return fail(errors.Join(validationErrors...))
	}
	if shellRuntime == "" {
		return fail(errors.New("built-in shell runtime is unavailable"))
	}
	translator, err := toolplugin.NewTranslator(ctx, pluginSnapshot.NodeExecutable, runtimeRoot, byName["shell"].Module)
	if err != nil {
		return fail(err)
	}
	return &toolRegistry{
		commandRouting:    &routing,
		builtinTranslator: translator,
		SnapshotDir:       snapshotDirectory,
		RuntimeRoot:       runtimeRoot,
		NodeExecutable:    pluginSnapshot.NodeExecutable,
		frontendDirectory: filepath.Join(snapshotDirectory, "bin"),
		runtimeDirectory:  runtimeDirectory,
		shellRuntime:      shellRuntime,
		ordered:           contributions,
		byName:            byName,
		wrappers:          wrappers,
		frontends:         frontends,
		DiagnoseHooks:     diagnoseHooks,
	}, nil
}

func validateToolContribution(contribution toolContribution) error {
	var specification struct {
		Type        string `json:"type"`
		Name        string `json:"name"`
		Description string `json:"description"`
		Format      *struct {
			Type       string `json:"type"`
			Syntax     string `json:"syntax"`
			Definition string `json:"definition"`
		} `json:"format"`
	}
	if err := json.Unmarshal(contribution.Specification, &specification); err != nil {
		return fmt.Errorf("%s/%s: decode tool specification: %w", contribution.PluginID, contribution.Name, err)
	}
	if specification.Type != "custom" || specification.Name != contribution.Name || specification.Description == "" {
		return fmt.Errorf("%s/%s: normalized custom-tool specification is inconsistent", contribution.PluginID, contribution.Name)
	}
	if specification.Format == nil {
		return nil
	}
	if specification.Format.Type != "grammar" ||
		(specification.Format.Syntax != "lark" && specification.Format.Syntax != "regex") ||
		specification.Format.Definition == "" {
		return fmt.Errorf("%s/%s: normalized grammar format is inconsistent", contribution.PluginID, contribution.Name)
	}
	return nil
}

func toolRegistryIdentity(manifest toolWorkerManifest, runtimePath string) (identity string, err error) {
	unsigned := manifest
	unsigned.RegistryID = ""
	encoded, err := json.Marshal(unsigned)
	if err != nil {
		return "", fmt.Errorf("encode tool registry identity: %w", err)
	}
	runtimeRoot, err := os.OpenRoot(runtimePath)
	if err != nil {
		return "", fmt.Errorf("open tool registry snapshot: %w", err)
	}
	defer func() { err = errors.Join(err, runtimeRoot.Close()) }()

	digest := sha256.New()
	_, _ = digest.Write(encoded)
	err = fs.WalkDir(runtimeRoot.FS(), ".", func(relative string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("tool registry snapshot entry %s is not a regular file", relative)
		}
		file, err := runtimeRoot.Open(relative)
		if err != nil {
			return err
		}
		before, statErr := file.Stat()
		if statErr != nil || !before.Mode().IsRegular() || !os.SameFile(info, before) {
			_ = file.Close()
			if statErr != nil {
				return statErr
			}
			return fmt.Errorf("tool registry snapshot entry %s changed during verification", relative)
		}
		fileDigest := sha256.New()
		copied, copyErr := io.Copy(fileDigest, file)
		after, afterErr := file.Stat()
		closeErr := file.Close()
		if copyErr != nil || afterErr != nil || closeErr != nil {
			return errors.Join(copyErr, afterErr, closeErr)
		}
		if !os.SameFile(before, after) || before.Size() != after.Size() || before.ModTime() != after.ModTime() ||
			copied != before.Size() {
			return fmt.Errorf("tool registry snapshot entry %s changed during verification", relative)
		}
		_, _ = digest.Write([]byte(filepath.ToSlash(relative)))
		_, _ = digest.Write([]byte{0})
		_, _ = digest.Write(fileDigest.Sum(nil))
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("hash tool registry snapshot: %w", err)
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func writeToolWorkerManifest(directory string, manifest toolWorkerManifest) error {
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("encode tool worker manifest: %w", err)
	}
	path := filepath.Join(directory, toolPluginManifestFilename)
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		return fmt.Errorf("write tool worker manifest: %w", err)
	}
	return nil
}

func (registry *toolRegistry) installFrontends() error {
	if registry == nil {
		return errors.New("tool registry is unavailable")
	}
	if len(registry.wrappers) == 0 {
		return nil
	}
	if err := os.MkdirAll(registry.frontendDirectory, 0o700); err != nil {
		return fmt.Errorf("create session tool frontends: %w", err)
	}
	fail := func(cause error) error {
		cleanupErr := removeWorkerFrontendSymlinks(registry.frontends, registry.wrappers)
		clear(registry.frontends)
		return errors.Join(cause, cleanupErr)
	}
	for _, contribution := range registry.ordered {
		wrapper, ok := registry.wrappers[contribution.Name]
		if !ok {
			continue
		}
		frontend, err := ensureWorkerSymlinkInDirectory(
			wrapper,
			registry.frontendDirectory,
			contribution.Name,
		)
		if err != nil {
			return fail(err)
		}
		registry.frontends[contribution.Name] = frontend
	}
	return nil
}

func (registry *toolRegistry) Close() error {
	if registry == nil {
		return nil
	}
	registry.closeOnce.Do(func() {
		registry.builtinTranslator.Close()
		registry.closeErr = errors.Join(
			removeWorkerFrontendSymlinks(registry.frontends, registry.wrappers),
			os.RemoveAll(registry.SnapshotDir),
		)

	})
	return registry.closeErr
}

func (registry *toolRegistry) wrapper(name string) (string, bool) {
	if registry == nil {
		return "", false
	}
	path, ok := registry.wrappers[name]
	return path, ok
}

func (registry *toolRegistry) contribution(name string) (toolContribution, bool) {
	if registry == nil {
		return toolContribution{}, false
	}
	contribution, ok := registry.byName[name]
	return contribution, ok
}

// specifications returns the tool specifications for all model-visible contributions.
func (registry *toolRegistry) specifications() ([]*responsesToolDefinition, error) {
	if registry == nil {
		return nil, errors.New("tool registry is unavailable")
	}
	specifications := make([]*responsesToolDefinition, 0, len(registry.ordered))
	for _, contribution := range registry.ordered {
		if !contribution.ModelVisible {
			continue
		}
		specification, err := decodeResponsesToolDefinition(contribution.Specification)
		if err != nil {
			return nil, fmt.Errorf("decode registered tool %s/%s: %w", contribution.PluginID, contribution.Name, err)
		}
		specifications = append(specifications, specification)
	}
	return specifications, nil
}

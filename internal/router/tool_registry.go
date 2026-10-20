package router

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/yusing/mekugi/internal/router/toolplugin"
	"github.com/yusing/mekugi/internal/runtimepath"
)

const (
	toolPluginManifestFilename = "workers.json"
)

func buildToolRegistry(ctx context.Context) (*toolRegistry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	runtimeDirectory, err := runtimepath.Directory()
	if err != nil {
		return nil, fmt.Errorf("locate tool runtime directory: %w", err)
	}
	replayDirectory, err := defaultMekugiReplayDirectory()
	if err != nil {
		return nil, err
	}
	return buildToolRegistryAt(ctx, runtimeDirectory, replayDirectory)
}

func buildToolRegistryAt(
	ctx context.Context,
	runtimeDirectory, replayDirectory string,
) (*toolRegistry, error) {
	if err := os.MkdirAll(runtimeDirectory, 0o700); err != nil {
		return nil, fmt.Errorf("create tool runtime directory: %w", err)
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
	var contributions []toolContribution
	var validationErrors []error
	for _, tool := range toolplugin.Builtins() {
		specification, err := decodeResponsesToolDefinition(tool.Specification)
		if err != nil {
			validationErrors = append(validationErrors, fmt.Errorf("decode frontend specification: %w", err))
			continue
		}
		contributions = append(contributions, toolContribution{
			Name: specification.Name, Specification: slices.Clone(tool.Specification),
			NativeExecutor: tool.NativeExecutor, Executable: true,
		})
	}
	byName := make(map[string]toolContribution, len(contributions))
	for _, contribution := range contributions {
		if len(contribution.Specification) != 0 {
			if validationErr := validateToolContribution(contribution); validationErr != nil {
				validationErrors = append(validationErrors, validationErr)
			}
		}
		if _, exists := byName[contribution.Name]; exists {
			validationErrors = append(validationErrors, fmt.Errorf("duplicate frontend name %q", contribution.Name))
			continue
		}
		byName[contribution.Name] = contribution
	}
	if len(validationErrors) != 0 {
		return fail(errors.Join(validationErrors...))
	}
	frontendGuidance, err := frontendGuidanceFromRegistry(contributions)
	if err != nil {
		return fail(err)
	}

	guidanceHash := sha256.Sum256([]byte(frontendGuidance))
	manifest := toolWorkerManifest{
		ReplayDirectory: replayDirectory,
		Version:         2,
		GuidanceSHA256:  hex.EncodeToString(guidanceHash[:]),
		Tools:           slices.Clone(contributions),
	}
	if debug, _ := ctx.Value(debugContextKey{}).(*debugOutput); debug != nil {
		manifest.AXReadOutput = debug.paths[4]
	}
	registryID, err := toolRegistryIdentity(manifest)
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
	if err := os.WriteFile(filepath.Join(snapshotDirectory, "frontend_guidance.md"), []byte(frontendGuidance), 0o600); err != nil {
		return fail(fmt.Errorf("write generated frontend guidance: %w", err))
	}
	if err := writeToolWorkerManifest(snapshotDirectory, manifest); err != nil {
		return fail(err)
	}
	for _, contribution := range contributions {
		if !contribution.Executable {
			continue
		}
		wrapper, wrapperErr := ensureWorkerSymlinkInDirectory(executable, snapshotDirectory, contribution.Name)
		if wrapperErr != nil {
			validationErrors = append(validationErrors, wrapperErr)
			continue
		}
		wrappers[contribution.Name] = wrapper
	}
	if len(validationErrors) != 0 {
		return fail(errors.Join(validationErrors...))
	}
	return &toolRegistry{
		SnapshotDir:       snapshotDirectory,
		frontendDirectory: filepath.Join(snapshotDirectory, "bin"),
		ordered:           contributions,
		frontendGuidance:  frontendGuidance,
		wrappers:          wrappers,
		frontends:         frontends,
	}, nil
}

func validateToolContribution(contribution toolContribution) error {
	var specification struct {
		Type        string          `json:"type"`
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Format      json.RawMessage `json:"format"`
	}
	if err := json.Unmarshal(contribution.Specification, &specification); err != nil {
		return fmt.Errorf("%s: decode frontend specification: %w", contribution.Name, err)
	}
	if specification.Type != "custom" || specification.Name != contribution.Name ||
		specification.Description == "" || len(specification.Format) != 0 ||
		contribution.NativeExecutor != contribution.Name {
		return fmt.Errorf("%s: compiled frontend specification is inconsistent", contribution.Name)
	}
	return nil
}

func toolRegistryIdentity(manifest toolWorkerManifest) (string, error) {
	manifest.RegistryID = ""
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return "", fmt.Errorf("encode frontend catalog identity: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
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
		registry.closeErr = errors.Join(
			removeWorkerFrontendSymlinks(registry.frontends, registry.wrappers),
			os.RemoveAll(registry.SnapshotDir),
		)

	})
	return registry.closeErr
}

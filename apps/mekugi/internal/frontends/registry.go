// Source: apps/mekugi/internal/router/tool_registry.go:1:249@[7b6d9ccc]
package frontends

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/command"
)

const (
	manifestFilename = "workers.json"
)

// Config supplies the compiled commands and invocation-owned publication paths.
// An empty RuntimeDirectory uses MEKUGI_RUNTIME_DIR or the OS temporary directory.
type Config struct {
	Commands         []command.Contribution
	Guidance         *binding.Guidance
	RuntimeDirectory string
	ReplayDirectory  string
	AXReadOutput     string
}

// New authenticates and publishes one private executable catalog before exposing
// its PATH entries. Close removes only this invocation's resources.
func New(ctx context.Context, config Config) (*Registry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	runtimeDirectory := config.RuntimeDirectory
	if runtimeDirectory == "" {
		runtimeDirectory = os.Getenv("MEKUGI_RUNTIME_DIR")
		if runtimeDirectory == "" {
			runtimeDirectory = os.TempDir()
		}
	}
	if !filepath.IsAbs(runtimeDirectory) {
		return nil, errors.New("locate tool runtime directory: MEKUGI_RUNTIME_DIR must be an absolute path")
	}
	runtimeDirectory = filepath.Clean(runtimeDirectory)
	commands, guidance := config.Commands, config.Guidance
	if err := os.MkdirAll(runtimeDirectory, 0o700); err != nil {
		return nil, fmt.Errorf("create tool runtime directory: %w", err)
	}
	snapshotDirectory, err := os.MkdirTemp(runtimeDirectory, "mekugi-tools-")
	if err != nil {
		return nil, fmt.Errorf("create tool registry snapshot: %w", err)
	}
	wrappers := make(map[string]string)
	frontends := make(map[string]string)
	fail := func(cause error) (*Registry, error) {
		return nil, errors.Join(
			cause,
			removeWorkerFrontendSymlinks(frontends, wrappers),
			os.RemoveAll(snapshotDirectory),
		)
	}
	if err := pinRunningExecutable(filepath.Join(snapshotDirectory, workerExecutableFilename)); err != nil {
		return fail(err)
	}
	var contributions []catalogEntry
	var validationErrors []error
	for _, spec := range commands {
		if err := spec.Validate(); err != nil {
			validationErrors = append(validationErrors, err)
			continue
		}
		wire := struct {
			Type        string `json:"type"`
			Name        string `json:"name"`
			Description string `json:"description"`
		}{"custom", spec.Name, spec.Description}
		encoded, err := json.Marshal(&wire)
		if err != nil {
			validationErrors = append(validationErrors, err)
			continue
		}
		contributions = append(contributions, catalogEntry{
			Name: spec.Name, Specification: encoded,
			NativeExecutor: spec.Name, Executable: true,
		})
	}
	byName := make(map[string]catalogEntry, len(contributions))
	for _, contribution := range contributions {
		if execute, _ := frontendExecutor(contribution, commands); execute == nil {
			validationErrors = append(validationErrors, fmt.Errorf("native executor %q is unavailable", contribution.NativeExecutor))
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
	var frontendGuidance binding.FrontendGuidance
	if guidance != nil {
		specs := make([]command.Spec, len(commands))
		for i, contribution := range commands {
			specs[i] = contribution.Spec
		}
		frontendGuidance, err = guidance.Frontends(specs)
		if err != nil {
			return fail(err)
		}
	}

	guidanceBytes, err := json.Marshal(&frontendGuidance)
	if err != nil {
		return fail(fmt.Errorf("encode frontend guidance: %w", err))
	}
	guidanceHash := sha256.Sum256(guidanceBytes)
	manifest := workerManifest{
		ReplayDirectory: config.ReplayDirectory,
		AXReadOutput:    config.AXReadOutput,
		Version:         2,
		GuidanceSHA256:  hex.EncodeToString(guidanceHash[:]),
		Tools:           slices.Clone(contributions),
	}
	registryID, err := registryIdentity(manifest)
	if err != nil {
		return fail(err)
	}
	manifest.RegistryID = registryID
	authenticatedDirectory := snapshotDirectory + "-" + registryID
	if err := os.Rename(snapshotDirectory, authenticatedDirectory); err != nil {
		return fail(fmt.Errorf("authenticate tool registry snapshot: %w", err))
	}
	snapshotDirectory = authenticatedDirectory
	executable := filepath.Join(snapshotDirectory, workerExecutableFilename)
	if err := os.WriteFile(filepath.Join(snapshotDirectory, "frontend_guidance.json"), guidanceBytes, 0o600); err != nil {
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
	registry := &Registry{
		SnapshotDir:       snapshotDirectory,
		ReplayDirectory:   config.ReplayDirectory,
		FrontendDirectory: filepath.Join(snapshotDirectory, "bin"),
		ordered:           contributions,
		FrontendGuidance:  frontendGuidance,
		Guidance:          guidance,
		wrappers:          wrappers,
		Frontends:         frontends,
	}
	if err := registry.installFrontends(); err != nil {
		return nil, errors.Join(err, registry.Close())
	}
	return registry, nil
}

func validateToolContribution(contribution catalogEntry) error {
	var specification struct {
		Type        string            `json:"type"`
		Name        string            `json:"name"`
		Description string            `json:"description"`
		Format      jsonv1.RawMessage `json:"format"`
	}
	if err := jsonv1.Unmarshal(contribution.Specification, &specification); err != nil {
		return fmt.Errorf("%s: decode frontend specification: %w", contribution.Name, err)
	}
	if specification.Type != "custom" || specification.Name != contribution.Name ||
		specification.Description == "" || len(specification.Format) != 0 ||
		contribution.NativeExecutor != contribution.Name {
		return fmt.Errorf("%s: compiled frontend specification is inconsistent", contribution.Name)
	}
	return nil
}

func registryIdentity(manifest workerManifest) (string, error) {
	manifest.RegistryID = ""
	encoded, err := jsonv1.Marshal(manifest)
	if err != nil {
		return "", fmt.Errorf("encode frontend catalog identity: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func writeToolWorkerManifest(directory string, manifest workerManifest) error {
	encoded, err := jsonv1.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("encode tool worker manifest: %w", err)
	}
	path := filepath.Join(directory, manifestFilename)
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		return fmt.Errorf("write tool worker manifest: %w", err)
	}
	return nil
}

func (registry *Registry) installFrontends() error {
	if len(registry.wrappers) == 0 {
		return nil
	}
	if err := os.MkdirAll(registry.FrontendDirectory, 0o700); err != nil {
		return fmt.Errorf("create session tool frontends: %w", err)
	}
	fail := func(cause error) error {
		cleanupErr := removeWorkerFrontendSymlinks(registry.Frontends, registry.wrappers)
		clear(registry.Frontends)
		return errors.Join(cause, cleanupErr)
	}
	for _, contribution := range registry.ordered {
		wrapper, ok := registry.wrappers[contribution.Name]
		if !ok {
			continue
		}
		frontend, err := ensureWorkerSymlinkInDirectory(
			wrapper,
			registry.FrontendDirectory,
			contribution.Name,
		)
		if err != nil {
			return fail(err)
		}
		registry.Frontends[contribution.Name] = frontend
	}
	return nil
}

func (registry *Registry) Close() error {
	if registry == nil {
		return nil
	}
	registry.closeOnce.Do(func() {
		registry.closeErr = errors.Join(
			removeWorkerFrontendSymlinks(registry.Frontends, registry.wrappers),
			os.RemoveAll(registry.SnapshotDir),
		)

	})
	return registry.closeErr
}

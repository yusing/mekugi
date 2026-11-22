// Source: apps/mekugi/internal/router/tool_registry.go:1:249@[7b6d9ccc]
package frontends

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	jsonv1 "encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/yusing/mekugi/contracts/command"
)

const (
	manifestFilename = "workers.json"
	manifestVersion  = 4
)

// Config supplies the compiled commands and invocation-owned publication paths.
// Publication uses MEKUGI_RUNTIME_DIR or the OS temporary directory.
type Config struct {
	Commands        []command.Contribution
	ReplayDirectory string
	WorkerImage     *WorkerImage
}

// ValidateCatalog checks the complete command catalog before activation.
func ValidateCatalog(commands []command.Contribution) error {
	var failures []error
	names := make(map[string]bool, len(commands))
	for _, contribution := range commands {
		if err := contribution.Validate(); err != nil {
			failures = append(failures, err)
		}
		if names[contribution.Name] {
			failures = append(failures, fmt.Errorf("duplicate frontend %q", contribution.Name))
		}
		names[contribution.Name] = true
		if contribution.Run == nil {
			failures = append(failures, fmt.Errorf("frontend %q has no compiled implementation", contribution.Name))
		}
	}
	return errors.Join(failures...)
}

// New authenticates and publishes one private executable catalog before exposing
// its PATH entries. Close removes only this invocation's resources.
func New(ctx context.Context, config Config) (*Registry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	runtimeDirectory := os.Getenv("MEKUGI_RUNTIME_DIR")
	if runtimeDirectory == "" {
		runtimeDirectory = os.TempDir()
	}
	if !filepath.IsAbs(runtimeDirectory) {
		return nil, errors.New("locate tool runtime directory: MEKUGI_RUNTIME_DIR must be an absolute path")
	}
	runtimeDirectory = filepath.Clean(runtimeDirectory)
	commands := config.Commands
	names := make([]string, len(commands))
	for i, contribution := range commands {
		names[i] = contribution.Name
	}
	if err := os.MkdirAll(runtimeDirectory, 0o700); err != nil {
		return nil, fmt.Errorf("create tool runtime directory: %w", err)
	}
	snapshotDirectory, err := os.MkdirTemp(runtimeDirectory, "mekugi-tools-")
	if err != nil {
		return nil, fmt.Errorf("create tool registry snapshot: %w", err)
	}
	fail := func(cause error) (*Registry, error) {
		return nil, errors.Join(cause, os.RemoveAll(snapshotDirectory))
	}
	if err := config.WorkerImage.pin(filepath.Join(snapshotDirectory, workerExecutableFilename)); err != nil {
		return fail(err)
	}
	manifest := workerManifest{
		ReplayDirectory: config.ReplayDirectory,
		Version:         manifestVersion,
		Tools:           names,
	}
	registryID := registryIdentity(manifest)
	manifest.RegistryID = registryID
	authenticatedDirectory := snapshotDirectory + "-" + registryID
	if err := os.Rename(snapshotDirectory, authenticatedDirectory); err != nil {
		return fail(fmt.Errorf("authenticate tool registry snapshot: %w", err))
	}
	snapshotDirectory = authenticatedDirectory
	executable := filepath.Join(snapshotDirectory, workerExecutableFilename)
	if err := writeToolWorkerManifest(snapshotDirectory, manifest); err != nil {
		return fail(err)
	}
	frontendDirectory := filepath.Join(snapshotDirectory, "bin")
	if len(names) != 0 {
		if err := os.Mkdir(frontendDirectory, 0o700); err != nil {
			return fail(fmt.Errorf("create session tool frontends: %w", err))
		}
	}
	for _, name := range names {
		frontend := filepath.Join(frontendDirectory, name)
		if err := os.Symlink(executable, frontend); err != nil {
			return fail(fmt.Errorf("create %s worker symlink: %w", name, err))
		}
	}
	return &Registry{
		SnapshotDir:       snapshotDirectory,
		ReplayDirectory:   config.ReplayDirectory,
		FrontendDirectory: frontendDirectory,
	}, nil
}

func registryIdentity(manifest workerManifest) string {
	manifest.RegistryID = ""
	encoded, _ := jsonv1.Marshal(manifest)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func writeToolWorkerManifest(directory string, manifest workerManifest) error {
	encoded, _ := jsonv1.Marshal(manifest)
	path := filepath.Join(directory, manifestFilename)
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		return fmt.Errorf("write tool worker manifest: %w", err)
	}
	return nil
}

func (registry *Registry) Close() error {
	if registry == nil {
		return nil
	}
	registry.closeOnce.Do(func() {
		registry.closeErr = os.RemoveAll(registry.SnapshotDir)
	})
	return registry.closeErr
}

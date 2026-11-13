package sessioncontrol

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"path/filepath"
	"slices"

	"github.com/yusing/mekugi/backends/codex/binding"
)

// NativeSkillCatalog borrows native discovery for an admitted runtime. It
// returns metadata only; the receiving source reader still owns file access.
func (r *Requests[T]) NativeSkillCatalog(ctx context.Context, thread string) (map[string][]string, error) {
	info, err := r.Thread(ctx, thread)
	if err != nil {
		return nil, err
	}
	settings, err := binding.ReadAdmissionSettings(thread, info.Path)
	if err != nil || !filepath.IsAbs(settings.Workspace) {
		return nil, errors.Join(errors.New("native skill workspace is unavailable"), err)
	}
	raw, err := r.call(ctx, thread, "skills/list", map[string]any{"cwds": []string{settings.Workspace}})
	if err != nil {
		return nil, err
	}
	var catalog binding.SkillCatalog
	if err := json.Unmarshal(raw, &catalog); err != nil {
		return nil, err
	}
	paths := make(map[string][]string)
	var failures []error
	found := false
	for _, entry := range catalog.Data {
		if filepath.Clean(entry.Cwd) != filepath.Clean(settings.Workspace) {
			continue
		}
		found = true
		for _, skill := range entry.Skills {
			if !skill.Enabled {
				continue
			}
			if skill.Name == "" || !filepath.IsAbs(skill.Path) {
				failures = append(failures, errors.New("native skill catalog contains an invalid identity"))
				continue
			}
			if !slices.Contains(paths[skill.Name], skill.Path) {
				paths[skill.Name] = append(paths[skill.Name], skill.Path)
			}
		}
		for _, failure := range entry.Errors {
			failures = append(failures, fmt.Errorf("native skill catalog: %s", failure.Message))
		}
	}
	if !found {
		failures = append(failures, errors.New("native skill catalog omitted the selected workspace"))
	}
	return paths, errors.Join(failures...)
}

package codexbinding

import (
	json "encoding/json/v2"

	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

// RoleCatalog projects the Codex roles from the child's frozen preparation.
// Role resolution is shared with spawn admission rather than Claude settings.
func RoleCatalog(settings control.Settings) (string, error) {
	var config effectiveConfig
	if err := json.Unmarshal(settings.Preparation, &config); err != nil {
		return "", err
	}
	roles, err := roleCatalog(config.Layers)
	if err != nil {
		return "", err
	}
	return roleGuidance(roles), nil
}

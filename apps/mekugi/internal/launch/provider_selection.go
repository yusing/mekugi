// Source: apps/mekugi/internal/launch/provider_selection.go@14a6d1f3.
package launch

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	launchoptions "github.com/yusing/mekugi/apps/mekugi/internal/launch/options"
	"github.com/yusing/mekugi/libraries/httptransport"
	"github.com/yusing/mekugi/providers/grok"
	"github.com/yusing/mekugi/providers/opencode"
)

// launchProviders resolves the same enabled providers for sessions and start options.
func launchProviders(ctx context.Context, options launchoptions.Config, openCode opencode.Config) (*grok.Auth, opencode.Config, error) {
	var auth *grok.Auth
	if options.Grok || options.ThirdParty {
		apiKey := strings.TrimSpace(os.Getenv("XAI_API_KEY"))
		path := options.GrokAuthFile
		if path == "" && apiKey == "" {
			directory, err := os.UserHomeDir()
			if err != nil && !options.ThirdParty {
				return nil, openCode, fmt.Errorf("locate Grok credentials: %w", err)
			}
			if err == nil {
				path = filepath.Join(directory, ".grok", "auth.json")
			}
		}
		candidate := grok.NewAuth(path, apiKey, httptransport.Client(nil))
		if _, err := candidate.Credentials(ctx); err == nil {
			auth = candidate
		} else if options.Grok {
			return nil, openCode, err
		}
	}
	if openCode.Enabled() {
		cachePath := ""
		if directory, err := os.UserCacheDir(); err == nil {
			cachePath = filepath.Join(directory, "mekugi", "opencode-models-v2.json")
		}
		openCode.Catalog = opencode.NewCatalog(cachePath, nil)
		if err := openCode.Catalog.Refresh(ctx, false); err != nil {
			log.Printf("OpenCode catalog: refresh/cache update unavailable; retaining last usable metadata")
		}
	}
	return auth, openCode, nil
}

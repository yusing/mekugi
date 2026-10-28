package launch

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/libraries/mcptransport"
)

// openInvocationMCP activates only the composed contributions. Tool policy and
// caller admission stay with each handler; the endpoint owns connection lifetime.
func openInvocationMCP(ctx context.Context, contributions []binding.MCPServer, executable string) (overrides []string, close func() error, err error) {
	var stops []func() error
	close = func() error {
		var failures []error
		for _, stop := range slices.Backward(stops) {
			failures = append(failures, stop())
		}
		return errors.Join(failures...)
	}
	for _, contribution := range contributions {
		var names []string
		for _, tool := range contribution.Tools {
			names = append(names, strconv.Quote(tool))
		}
		socket, stop, err := mcptransport.Start(ctx, contribution.Server)
		if err != nil {
			return nil, close, errors.Join(err, close())
		}
		stops = append(stops, stop)
		// These are invocation arguments, never writes to the caller's config.
		// Explicit tool lists override a caller's stale restriction on this owned
		// server only. Host execution/approval policy is unchanged.
		// Declare invocation-owned tools eagerly inside the stock exec catalog.
		overrides = append(overrides, "-c", fmt.Sprintf("mcp_servers.%s={command=%s,args=[\"mcp-bridge\",%s],enabled=true,required=true,enabled_tools=[%s],disabled_tools=[],omit_tools_from=[\"deferred\"],default_tools_approval_mode=\"approve\"}",
			contribution.Name, strconv.Quote(executable), strconv.Quote(socket), strings.Join(names, ",")))
	}
	return overrides, close, nil
}

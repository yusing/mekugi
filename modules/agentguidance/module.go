package agentguidance

import (
	"context"
	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/contracts/recordkind"
	"github.com/yusing/mekugi/modules/agentguidance/codexbinding"
	"github.com/yusing/mekugi/modules/agentguidance/compact"
	"github.com/yusing/mekugi/modules/agentguidance/internal/policy"
	"github.com/yusing/mekugi/modules/changehistory/changes"
)

var Module = module.Descriptor[Deps, *policy.Service, codexbinding.Bindings, module.NoViews]{
	ID: "agentguidance", Summary: "Assemble agent guidance without changing caller authority", Lifetime: module.Stateless,
	Requires: []module.ID{"sessionhistory", "changehistory"},
	Contributes: module.Contributions[*policy.Service]{RecordKinds: []recordkind.Spec{compact.Kind}, Commands: []command.Definition[*policy.Service]{
		{Name: "mreload", Description: "Capture selected files, skills and mchanges diffs for compaction reload or delegate preload. Usage: mreload SELECTION_JSON. Returns a retained snapshot reference after all sources finish.", Run: func(service *policy.Service, ctx context.Context, request command.Request) (command.Output, error) {
			reader := compact.ChangeReader{Service: service.Changes}
			if service.History != nil && request.Scope.Directory != "" {
				reader.Scope = changes.Scope{Store: service.History.Attach(request.Scope.Directory), Namespace: request.Scope.Namespace, Thread: request.Scope.Thread}
			}
			return compact.Capture(ctx, request, reader)
		}},
	}},
	HostsGuidance: true, New: newService, Bindings: codexbinding.Codex,
}

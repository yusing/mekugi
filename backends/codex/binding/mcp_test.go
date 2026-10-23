package binding

import (
	"errors"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yusing/mekugi/contracts/capability"
)

func (r *referenceRegistrar) MCP(MCPServer) error { return r.record(capability.MCPTools) }

func TestComposedMCPRegistration(t *testing.T) {
	registry := new(Registry)
	server := MCPServer{Name: "threadcontrol", Server: mcp.NewServer(&mcp.Implementation{Name: "threadcontrol", Version: "1"}, nil), Tools: []string{"list_agents"}}
	factoryCalls := 0
	bindings := Bindings[*int]{MCP: func(service *int) MCPServer {
		factoryCalls++
		if *service != 7 {
			t.Fatal("wrong owner supplied")
		}
		return server
	}}
	reference := bindings.Reference()
	if len(reference.Mechanisms) != 1 || reference.Mechanisms[0] != capability.MCPTools || factoryCalls != 0 {
		t.Fatalf("reference invoked construction: %+v", reference)
	}
	if err := bindings.Register(registry, new(7), nil); err != nil {
		t.Fatal(err)
	}
	if factoryCalls != 1 {
		t.Fatal("registration did not construct the contribution once")
	}
	registered := registry.RegisteredMCP()
	if len(registered) != 1 || registered[0].Server != server.Server {
		t.Fatal(registered)
	}
	registered[0].Tools[0] = "changed"
	server.Tools[0] = "also changed"
	if got := registry.RegisteredMCP()[0].Tools[0]; got != "list_agents" {
		t.Fatal("snapshot mutated registration")
	}
	if err := registry.MCP(server); err == nil {
		t.Fatal("duplicate server accepted")
	}
	failed := errors.New("registration failed")
	registrar := &referenceRegistrar{failAt: 1, failure: failed}
	if err := bindings.Register(registrar, new(7), nil); !errors.Is(err, failed) {
		t.Fatalf("registration failure lost: %v", err)
	}
}

func TestMCPRegistrationRejectsIncompleteContributions(t *testing.T) {
	for _, server := range []MCPServer{{}, {Name: "threadcontrol"}, {Name: "threadcontrol", Tools: []string{"spawn_agent"}}, {Name: "invalid.key", Server: mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, nil), Tools: []string{"spawn_agent"}}} {
		if err := new(Registry).MCP(server); err == nil {
			t.Fatal("accepted incomplete MCP registration")
		}
	}
}

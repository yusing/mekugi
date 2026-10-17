package composition

import (
	"reflect"
	"slices"
	"testing"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/activitydetail"
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/contracts/scope"
)

func TestCompositionActivityDetails(t *testing.T) {
	for _, omit := range []string{"none", "first", "second", "both"} {
		t.Run(omit, func(t *testing.T) {
			type service struct{ value string }
			var c Composition
			var names []string
			host := c.Add(module.Descriptor[struct{}, []activitydetail.Provider, module.NoBindings]{
				ID: "host", Lifetime: module.Stateless, HostsActivityDetails: true,
				New: func(_ struct{}, slots module.Slots) ([]activitydetail.Provider, error) {
					var got []string
					for _, provider := range slots.ActivityDetails {
						got = append(got, provider.Name)
					}
					if !slices.Equal(got, names) {
						t.Fatalf("host constructed with providers %v, want complete list %v", got, names)
					}
					return slots.ActivityDetails, nil
				},
			}, emptyDeps)
			store := new(scope.Store)
			request := activitydetail.Request{Operation: activitydetail.Operation{Thread: "thread", Item: "item"}, Path: "directory", Directory: true}
			var handles []*Handle[*service]
			var want []activitydetail.Result
			calls := 0
			for _, id := range []string{"first", "second"} {
				if omit == id || omit == "both" {
					continue
				}
				var definitions []activitydetail.Definition[*service]
				for _, name := range []string{id + "-a", id + "-b"} {
					names = append(names, name)
					definitions = append(definitions, activitydetail.Definition[*service]{
						Name: name,
						Resolve: func(s *service, gotStore *scope.Store, gotRequest activitydetail.Request) activitydetail.Result {
							calls++
							if gotStore != store || gotRequest != request {
								t.Fatal("resolver lost the caller's scope or request")
							}
							return activitydetail.Result{Pages: []activitydetail.Page{{ID: name, Code: s.value, Path: gotRequest.Path}}, Selected: name}
						},
					})
					want = append(want, activitydetail.Result{Pages: []activitydetail.Page{{ID: name, Code: id + "-live", Path: request.Path}}, Selected: name})
				}
				handles = append(handles, c.Add(module.Descriptor[struct{}, *service, module.NoBindings]{
					ID: module.ID(id), Lifetime: module.Stateless,
					Contributes: module.Contributions[*service]{ActivityDetails: definitions},
					New: func(_ struct{}, slots module.Slots) (*service, error) {
						if slots.ActivityDetails != nil {
							t.Fatal("non-host received Activity detail providers")
						}
						if len(host.Value()) != len(names) {
							t.Fatal("earlier host did not receive later providers before contributor construction")
						}
						return &service{value: id}, nil
					},
				}, emptyDeps))
			}
			runtime, err := c.Activate(t.Context(), new(binding.Registry))
			if err != nil {
				t.Fatal(err)
			}
			if calls != 0 {
				t.Fatalf("resolvers called during activation: %d", calls)
			}
			for _, handle := range handles {
				handle.Value().value += "-live"
			}
			for i, provider := range host.Value() {
				if got := provider.Resolve(store, request); !reflect.DeepEqual(got, want[i]) {
					t.Fatalf("provider %q result=%+v, want %+v", provider.Name, got, want[i])
				}
			}
			if calls != len(want) {
				t.Fatalf("resolver calls=%d, want %d", calls, len(want))
			}
			if err := runtime.Close(t.Context()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCompositionActivityDetailsRetainsMissingResolver(t *testing.T) {
	var c Composition
	c.Add(module.Descriptor[struct{}, struct{}, module.NoBindings]{
		ID: "host", Lifetime: module.Stateless, HostsActivityDetails: true,
		New: func(_ struct{}, slots module.Slots) (struct{}, error) {
			if len(slots.ActivityDetails) != 1 || slots.ActivityDetails[0].Name != "missing" || slots.ActivityDetails[0].Resolve != nil {
				t.Fatalf("host cannot validate missing resolver: %+v", slots.ActivityDetails)
			}
			return struct{}{}, nil
		},
	}, emptyDeps)
	contributor := descriptor("contributor")
	contributor.Contributes.ActivityDetails = []activitydetail.Definition[*testService]{{Name: "missing"}}
	c.Add(contributor, emptyDeps)
	runtime, err := c.Activate(t.Context(), new(binding.Registry))
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

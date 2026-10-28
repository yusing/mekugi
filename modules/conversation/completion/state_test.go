package completion

import (
	"testing"

	"github.com/yusing/mekugi/modules/conversation/composer"
)

func TestTargetUsesWholeTokenAndExplicitBindings(t *testing.T) {
	p := State{}
	d := composer.Draft{Text: "界 @src/文件.go tail", CursorBack: len("文件.go tail")}
	want := Target{Kind: '@', Start: len("界 "), End: len("界 @src/文件.go"), Query: "src/文件.go"}
	if got := p.FindTarget(d); got != want {
		t.Fatalf("target=%+v, want %+v", got, want)
	}
	d.Files = []composer.Token{{Start: want.Start, End: want.End, Path: "src/文件.go"}}
	if got := p.FindTarget(d); got != (Target{}) {
		t.Fatalf("bound file offered again: %+v", got)
	}
	for _, literal := range []string{"$PATH", "$123", "$-", "$?"} {
		if got := p.FindTarget(composer.Draft{Text: literal}); got != (Target{}) {
			t.Fatalf("literal %q became a target: %+v", literal, got)
		}
	}
}

func TestDismissalAndResultAdmission(t *testing.T) {
	target := Target{Kind: '@', End: 4, Query: "src"}
	p := State{Dismissed: target}
	if p.Activate(target, false) || p.Open {
		t.Fatal("dismissed target reopened")
	}
	next := Target{Kind: '@', End: 5, Query: "src/"}
	if !p.Activate(next, false) {
		t.Fatal("changed target stayed dismissed")
	}
	p.Loading = true
	choices := []Choice{{Name: "src/a", Path: "src/a"}}
	if p.Resolve(target, "/one", "/one", choices, "") || p.Resolve(next, "/one", "/two", choices, "") || !p.Loading {
		t.Fatal("stale query or workspace changed foreground results")
	}
	if !p.Resolve(next, "/one", "/one", choices, "") || p.Loading || p.Resolved != next {
		t.Fatalf("current result not admitted: %+v", p)
	}
	view := p.View()
	view.Choices[0].Path = "mutated"
	if p.Choices[0].Path != "src/a" {
		t.Fatal("view aliases completion state")
	}
}

func TestSkillCatalogFilteringAndAcknowledgedToggle(t *testing.T) {
	p := State{}
	p.Catalog([]Choice{
		{Name: "plugin:build", Display: "Build", Path: "/skills/build", Enabled: true},
		{Name: "review", Path: "/skills/review"},
		{Name: "managed", Enabled: true},
	}, "/workspace", true, "partial catalog")
	p.RememberSkills()
	p.FilterSkills("plugin", false)
	if len(p.Choices) != 1 || p.Choices[0].Name != "plugin:build" || p.SkillsProblem != "" {
		t.Fatalf("canonical filter or available catalog lost: %+v", p)
	}
	p.FilterSkills("", true)
	if len(p.Choices) != 2 {
		t.Fatalf("management must include disabled native entries only: %+v", p.Choices)
	}
	p.SkillSaved("/workspace", "/skills/review", true)
	if enabled, disabled := p.SkillChanges(); enabled != 1 || disabled != 0 {
		t.Fatalf("toggle summary=%d/%d", enabled, disabled)
	}
	p.FilterSkills("review", false)
	if len(p.Choices) != 1 || !p.Choices[0].Enabled {
		t.Fatalf("acknowledged toggle missing: %+v", p.Choices)
	}
}

func TestPresetChoicesStayDistinctFromSkillBindings(t *testing.T) {
	p := State{Skills: []Choice{{Name: "review", Path: "/skills/review", Enabled: true}}, Presets: []Choice{
		{ID: 2, Name: "review", Preset: true}, {ID: 3, Name: "review", Preset: true},
	}}
	p.FilterSkills("rev", false)
	p.IncludePresets("rev")
	p.IncludePresets("rev")
	if len(p.Choices) != 3 || p.Choices[0].Identity() == p.Choices[1].Identity() || p.Choices[1].Identity() == p.Choices[2].Identity() {
		t.Fatalf("preset/skill identities overlap or repeated publication duplicates them: %+v", p.Choices)
	}
	p.FilterSkills("rev", true)
	if len(p.Choices) != 1 || p.Choices[0].Preset {
		t.Fatalf("presets entered skill management: %+v", p.Choices)
	}
	if len(p.Skills) != 1 || p.Skills[0].Preset || p.Skills[0].Path != "/skills/review" {
		t.Fatalf("preset displaced the binding catalog: %+v", p.Skills)
	}
}

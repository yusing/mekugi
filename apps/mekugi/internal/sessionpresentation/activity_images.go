package sessionpresentation

import (
	"github.com/yusing/mekugi/contracts/observation"
	"github.com/yusing/mekugi/libraries/pathdisplay"
	"github.com/yusing/mekugi/libraries/sourceanalysis/markdownsyntax"
)

// ImagePresentation shares artifact admission across live, restored and replayed
// items. Renderer capability never changes the semantic text or source binding.
func ImagePresentation(item observation.Item, phase, workspace string) (string, []string) {
	verb, path := "View", item.Path
	if item.Type == "imageGeneration" {
		if phase != "item/completed" {
			return "", nil
		}
		verb, path = "Generated", item.SavedPath
	}
	if path == "" {
		return "", nil
	}
	return verb + " " + markdownsyntax.QuoteCode(pathdisplay.ForWorkspace(workspace, path)), []string{path}
}

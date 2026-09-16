package codexinstructions

import (
	"embed"
	"strings"
)

//go:embed help/*.md
var helpFiles embed.FS

const helpIndex = `Mekugi topic help: hhelp TOPIC
  shell     explicit batches, program input, output selection, retained scripts
  read      output framing, previews, file batches, semantic selectors
  journal   list/delete/batch, IDs, and answer attachments
  recovery  rejected-script correction payloads
  changes   diff summaries, history, and filters
Use help only when the task needs these options; ordinary work needs no help call.
`

// Help returns bundled guidance without workspace or session dependencies.
// An empty topic returns the topic index.
func Help(topic string) (string, bool) {
	if topic == "" {
		return helpIndex, true
	}
	if strings.ContainsAny(topic, "/\\.") {
		return "", false
	}
	data, err := helpFiles.ReadFile("help/" + topic + ".md")
	return string(data), err == nil
}

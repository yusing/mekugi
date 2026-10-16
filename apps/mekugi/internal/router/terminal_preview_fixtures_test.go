package router

import (
	"strconv"
	"strings"
)

// previewFiles seed the workspace that the replayed patches edit.
var previewFiles = map[string]string{
	"internal/broker/broker.go": `package broker

import "sync"

type Broker struct {
	mu   sync.Mutex
	subs map[string][]chan string
}

func New() *Broker { return &Broker{subs: make(map[string][]chan string)} }

func (b *Broker) Subscribe(topic string) chan string {
	ch := make(chan string, 1)
	b.subs[topic] = append(b.subs[topic], ch)
	return ch
}

func (b *Broker) Publish(topic, message string) {
	for _, ch := range b.subs[topic] {
		ch <- message
	}
}
`,
	"internal/pane/launch.go": `package pane

// Launch opens the side pane for a requested preview.
func Launch(requested bool, frames <-chan string) string {
	if requested {
		return "open"
	}
	return <-frames
}
`,
	"internal/broker/broker_test.go": `package broker

import "testing"

func TestPublish(t *testing.T) {
	b := New()
	b.Publish("topic", "hello")
}
`,
}

// previewPatches are streamed in order, as a worker's apply_patch calls.
var previewPatches = []string{
	`*** Begin Patch
*** Update File: internal/broker/broker_test.go
@@
 func TestPublish(t *testing.T) {
 	b := New()
 	b.Publish("topic", "hello")
 }
+
+func TestSubscribeBeforePublish(t *testing.T) {
+	b := New()
+	messages := b.Subscribe("topic")
+	b.Publish("topic", "hello")
+	if got := <-messages; got != "hello" {
+		t.Fatalf("got %q", got)
+	}
+}
+
+func TestUnsubscribeRace(t *testing.T) {
+	b := New()
+	messages := b.Subscribe("topic")
+	go b.Publish("topic", "late")
+	b.Unsubscribe("topic", messages)
+}
*** End Patch
`,
	`*** Begin Patch
*** Update File: internal/broker/broker.go
@@
 func (b *Broker) Subscribe(topic string) chan string {
 	ch := make(chan string, 1)
+	b.mu.Lock()
+	defer b.mu.Unlock()
 	b.subs[topic] = append(b.subs[topic], ch)
 	return ch
 }
 
 func (b *Broker) Publish(topic, message string) {
-	for _, ch := range b.subs[topic] {
+	b.mu.Lock()
+	subs := append([]chan string(nil), b.subs[topic]...)
+	b.mu.Unlock()
+	for _, ch := range subs {
 		ch <- message
 	}
 }
+
+func (b *Broker) Unsubscribe(topic string, ch chan string) {
+	b.mu.Lock()
+	defer b.mu.Unlock()
+	subs := b.subs[topic]
+	for i, c := range subs {
+		if c == ch {
+			b.subs[topic] = append(subs[:i], subs[i+1:]...)
+			return
+		}
+	}
+}
*** End Patch
`,
	`*** Begin Patch
*** Add File: internal/broker/doc.go
+// Package broker fans published messages out to topic subscribers.
+package broker
*** End Patch
`,
}

// applyPreviewPatch applies one single-hunk fixture patch to before.
func applyPreviewPatch(patch, before string) (path, after string, added, removed int, ok bool) {
	var old, next strings.Builder
	create := false
	for line := range strings.Lines(patch) {
		switch {
		case strings.HasPrefix(line, "*** Update File: "):
			path = strings.TrimSpace(strings.TrimPrefix(line, "*** Update File: "))
		case strings.HasPrefix(line, "*** Add File: "):
			path, create = strings.TrimSpace(strings.TrimPrefix(line, "*** Add File: ")), true
		case strings.HasPrefix(line, "***"), strings.HasPrefix(line, "@@"):
		case strings.HasPrefix(line, "+"):
			next.WriteString(line[1:])
			added++
		case strings.HasPrefix(line, "-"):
			old.WriteString(line[1:])
			removed++
		case strings.HasPrefix(line, " "):
			old.WriteString(line[1:])
			next.WriteString(line[1:])
		case line == "\n":
			old.WriteString(line)
			next.WriteString(line)
		}
	}
	if create {
		return path, next.String(), added, removed, true
	}
	if !strings.Contains(before, old.String()) {
		return path, "", 0, 0, false
	}
	return path, strings.Replace(before, old.String(), next.String(), 1), added, removed, true
}

func previewPythonEditCommand() (oldFlow, newFlow, pythonCommand string) {
	oldFlow = "\tif requested {\n\t\treturn \"open\"\n\t}\n\treturn <-frames"
	newFlow = "\tframe := <-frames\n\tif requested {\n\t\treturn \"open: \" + frame\n\t}\n\treturn frame"
	pythonCommand = "python3 - <<'PY'\nfrom pathlib import Path\np = Path('internal/pane/launch.go')\ns = p.read_text()\ns = s.replace(" + strconv.Quote(oldFlow) + ", " + strconv.Quote(newFlow) + ", 1)\np.write_text(s)\nPY\n"
	return
}

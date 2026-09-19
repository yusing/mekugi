package main

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"

	"github.com/tiktoken-go/tokenizer"
	"github.com/yusing/mekugi"
	"github.com/yusing/mekugi/internal/patchtest"
)

type scenario struct {
	name    string
	initial map[string]string
	edits   []mekugi.FileEdit
	patch   string
}

func main() {
	codec, err := tokenizer.ForModel(tokenizer.GPT5)
	if err != nil {
		fatalf("loading GPT-5 tokenizer: %v", err)
	}

	fmt.Printf("GPT-5 encoding: %s\n\n", codec.GetName())
	fmt.Printf("%-28s %8s %12s %8s %11s\n", "scenario", "mekugi", "apply_patch", "saved", "reduction")

	var totalMekugi, totalApplyPatch int
	for _, scenario := range scenarios() {
		mekugiTree, err := runMekugi(scenario)
		if err != nil {
			fatalf("%s: %v", scenario.name, err)
		}
		patchTree, err := patchtest.Apply(scenario.initial, scenario.patch)
		if err != nil {
			fatalf("%s apply_patch input: %v", scenario.name, err)
		}
		if !reflect.DeepEqual(mekugiTree, patchTree) {
			fatalf("%s representations differ:\nmekugi: %#v\napply_patch: %#v", scenario.name, mekugiTree, patchTree)
		}

		payload, err := json.Marshal(&scenario.edits)
		if err != nil {
			fatalf("encoding %s mekugi input: %v", scenario.name, err)
		}
		mekugiTokens, err := codec.Count(string(payload))
		if err != nil {
			fatalf("tokenizing %s mekugi input: %v", scenario.name, err)
		}
		patchTokens, err := codec.Count(scenario.patch)
		if err != nil {
			fatalf("tokenizing %s apply_patch input: %v", scenario.name, err)
		}
		totalMekugi += mekugiTokens
		totalApplyPatch += patchTokens
		printRow(scenario.name, mekugiTokens, patchTokens)
	}

	fmt.Println()
	printRow("total", totalMekugi, totalApplyPatch)
}

func scenarios() []scenario {
	return []scenario{
		{
			name: "long-line replacement",
			initial: map[string]string{
				"calc.go": "package calc\n\nfunc total(subtotal, tax int) int { return subtotal + tax + adjustmentForRegion(subtotal, tax) }\n",
			},
			edits: []mekugi.FileEdit{{Path: "calc.go", Script: "type 3:db22 \"subtotal + tax\" \"subtotal - discount + tax\"\n"}},
			patch: "*** Begin Patch\n*** Update File: calc.go\n@@\n-func total(subtotal, tax int) int { return subtotal + tax + adjustmentForRegion(subtotal, tax) }\n+func total(subtotal, tax int) int {\n+\treturn subtotal - discount + tax + adjustmentForRegion(subtotal, tax)\n+}\n*** End Patch\n",
		},
		{
			name:    "last occurrence delete",
			initial: map[string]string{"logs.txt": "debug info debug\n"},
			edits:   []mekugi.FileEdit{{Path: "logs.txt", Script: "type 1:22b6 \" debug\" \"\"\n"}},
			patch:   "*** Begin Patch\n*** Update File: logs.txt\n@@\n-debug info debug\n+debug info\n*** End Patch\n",
		},
		{
			name: "block duplication",
			initial: map[string]string{
				"service.go": "func run() {\n\tprepare()\n\texecute()\n}\n",
			},
			edits: []mekugi.FileEdit{{Path: "service.go", Script: "add 4:d10b \"\\tprepare()\\n\\texecute()\\n\"\n"}},
			patch: "*** Begin Patch\n*** Update File: service.go\n@@\n \tprepare()\n \texecute()\n+\tprepare()\n+\texecute()\n*** End Patch\n",
		},
		{
			name:    "stable baseline hashes",
			initial: map[string]string{"config.txt": "name=old\nmode=slow\n"},
			edits:   []mekugi.FileEdit{{Path: "config.txt", Script: "type 1:165f \"old\" \"new\\nextra=yes\"\ntype 2:763c \"slow\" \"fast\"\n"}},
			patch:   "*** Begin Patch\n*** Update File: config.txt\n@@\n-name=old\n-mode=slow\n+name=new\n+extra=yes\n+mode=fast\n*** End Patch\n",
		},
		{
			name:    "append to existing file",
			initial: map[string]string{"note.txt": "intro\n"},
			edits:   []mekugi.FileEdit{{Path: "note.txt", Script: "append \"foo bar\\n\"\n"}},
			patch:   "*** Begin Patch\n*** Update File: note.txt\n@@\n intro\n+foo bar\n*** End Patch\n",
		},
		{
			name: "multi-file replacement",
			initial: map[string]string{
				"old.txt":      "hello old\n",
				"obsolete.txt": "unused\n",
			},
			edits: []mekugi.FileEdit{
				{Path: "old.txt", Script: "type 1:53e5 \"old\" \"new\"\n"},
				{Path: "obsolete.txt", Script: "type \"unused\" \"used\"\n"},
			},
			patch: "*** Begin Patch\n*** Update File: old.txt\n@@\n-hello old\n+hello new\n*** Update File: obsolete.txt\n@@\n-unused\n+used\n*** End Patch\n",
		},
	}
}

func runMekugi(scenario scenario) (map[string]string, error) {
	root, err := os.MkdirTemp("", "mekugi-compare-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(root)
	for path, content := range scenario.initial {
		if err := os.WriteFile(filepath.Join(root, path), []byte(content), 0o644); err != nil {
			return nil, err
		}
	}
	workspaceRoot, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer workspaceRoot.Close()
	if err := mekugi.Apply(context.TODO(), mekugi.Workspace{Root: workspaceRoot}, scenario.edits); err != nil {
		return nil, fmt.Errorf("applying HPATCH script: %w", err)
	}
	return readTree(root)
}

func readTree(root string) (map[string]string, error) {
	tree := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		tree[relative] = string(content)
		return nil
	})
	return tree, err
}

func printRow(name string, mekugiTokens, patchTokens int) {
	saved := patchTokens - mekugiTokens
	reduction := 0.0
	if patchTokens != 0 {
		reduction = float64(saved) / float64(patchTokens) * 100
	}
	fmt.Printf("%-28s %8d %12d %8d %10.1f%%\n", name, mekugiTokens, patchTokens, saved, reduction)
}

func fatalf(format string, arguments ...any) {
	fmt.Fprintf(os.Stderr, "compare: "+format+"\n", arguments...)
	os.Exit(1)
}

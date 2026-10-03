package router

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/livediff"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
)

const temporaryPlanSetup = "batch_plan_dir=$(mktemp -d /tmp/mekugi-batches.XXXXXX)\nexport MEKUGI_BATCH_PLAN_DIR=\"$batch_plan_dir\"\n"
const temporaryPlanSource = `import json, os
from pathlib import Path
root = Path(os.environ["MEKUGI_BATCH_PLAN_DIR"])
(root / "plan.json").write_text(json.dumps({"plan": []}, ensure_ascii=False, indent=2))
(root / "ci-failure.log").write_text("diagnostic")
print(root / "plan.json")
`

func temporaryPlanCommand(setup, source string) string {
	return setup + "python3 - <<'PY'\n" + source + "PY\n"
}

func TestTemporaryWriteActivityIntent(t *testing.T) {
	for _, tc := range []struct {
		name, setup, source string
		unknown, workspace  bool
	}{
		{name: "confirmed temporary artifacts", setup: temporaryPlanSetup, source: temporaryPlanSource},
		{name: "original failed log prefix", setup: temporaryPlanSetup + "gh run view 37104437662 --log-failed > \"$batch_plan_dir/ci-failure.log\"\n", source: temporaryPlanSource},
		{name: "failed log prefix with unresolved edit", setup: temporaryPlanSetup + "gh run view 37104437662 --log-failed > \"$batch_plan_dir/ci-failure.log\"\n", source: temporaryPlanSource + "Path(dynamic).write_text('code')\n", unknown: true},
		{name: "builtin log prefix rebinds environment", setup: temporaryPlanSetup + "read MEKUGI_BATCH_PLAN_DIR > \"$batch_plan_dir/ci-failure.log\"\n", source: temporaryPlanSource, unknown: true},
		{name: "log prefix has expansion effects", setup: temporaryPlanSetup + "gh run view 37104437662 --log-failed > \"${MEKUGI_BATCH_PLAN_DIR:=.}/ci-failure.log\"\n", source: temporaryPlanSource, unknown: true},
		{name: "log prefix allocates environment descriptor", setup: temporaryPlanSetup + "gh run view 37104437662 --log-failed {MEKUGI_BATCH_PLAN_DIR}> \"$batch_plan_dir/ci-failure.log\"\n", source: temporaryPlanSource, unknown: true},
		{name: "other temporary names", setup: strings.ReplaceAll(temporaryPlanSetup, "mekugi-batches", "arbitrary"), source: strings.ReplaceAll(temporaryPlanSource, "plan.json", "code.go")},
		{name: "unrelated exported value", setup: "export OTHER=literal\n", source: "from pathlib import Path\nPath('workspace.go', 'child').write_text('code')\n", workspace: true},
		{name: "workspace and temporary", setup: temporaryPlanSetup, source: temporaryPlanSource + "Path('workspace.go').write_text('code')\n", workspace: true},
		{name: "unresolved sibling", setup: temporaryPlanSetup, source: temporaryPlanSource + "Path(dynamic).write_text('code')\n", unknown: true},
		{name: "no environment proof", source: temporaryPlanSource, unknown: true},
		{name: "temporary looking workspace name", setup: "batch_plan_dir=plan.json\nexport MEKUGI_BATCH_PLAN_DIR=\"$batch_plan_dir\"\n", source: temporaryPlanSource, unknown: true},
		{name: "unexported", setup: "MEKUGI_BATCH_PLAN_DIR=$(mktemp -d /tmp/artifacts.XXXXXX)\n", source: temporaryPlanSource, unknown: true},
		{name: "non temporary mktemp directory", setup: strings.ReplaceAll(temporaryPlanSetup, "/tmp/mekugi-batches", "/workspace/mekugi-batches"), source: temporaryPlanSource, unknown: true},
		{name: "reassigned shell root", setup: temporaryPlanSetup + "batch_plan_dir=workspace\nexport MEKUGI_BATCH_PLAN_DIR=\"$batch_plan_dir\"\n", source: temporaryPlanSource, unknown: true},
		{name: "reassigned exported root", setup: temporaryPlanSetup + "MEKUGI_BATCH_PLAN_DIR=workspace\n", source: temporaryPlanSource, unknown: true},
		{name: "arithmetic mutates shell root", setup: temporaryPlanSetup + "x=$((MEKUGI_BATCH_PLAN_DIR=1))\n", source: temporaryPlanSource, unknown: true},
		{name: "shell function replaces mktemp", setup: "mktemp() { echo workspace; }\n" + temporaryPlanSetup, source: temporaryPlanSource, unknown: true},
		{name: "source changes environment", setup: temporaryPlanSetup, source: strings.ReplaceAll(temporaryPlanSource, "root =", "os.environ['MEKUGI_BATCH_PLAN_DIR'] = 'workspace'\nroot ="), unknown: true},
		{name: "parent traversal", setup: temporaryPlanSetup, source: strings.ReplaceAll(temporaryPlanSource, "plan.json", "../workspace.go"), unknown: true},
		{name: "join parent traversal", setup: temporaryPlanSetup, source: "import os\nfrom pathlib import Path\nPath(os.path.join(os.environ['MEKUGI_BATCH_PLAN_DIR'], '../workspace.go')).write_text('code')\n", unknown: true},
		{name: "rebound Path", setup: temporaryPlanSetup, source: "import os\nfrom pathlib import Path\nPath = get_path_constructor()\nPath(os.environ['MEKUGI_BATCH_PLAN_DIR']).write_text('code')\n", unknown: true},
		{name: "environment alias mutation", setup: temporaryPlanSetup, source: strings.ReplaceAll(temporaryPlanSource, "root =", "environment = os.environ\nenvironment['MEKUGI_BATCH_PLAN_DIR'] = '.'\nroot ="), unknown: true},
		{name: "module alias mutation", setup: temporaryPlanSetup, source: strings.ReplaceAll(temporaryPlanSource, "root =", "module = os\nmodule.environ['MEKUGI_BATCH_PLAN_DIR'] = '.'\nroot ="), unknown: true},
		{name: "environment escapes as argument", setup: temporaryPlanSetup, source: strings.ReplaceAll(temporaryPlanSource, "root =", "update_environment(os.environ)\nroot ="), unknown: true},
		{name: "module imported alias mutation", setup: temporaryPlanSetup, source: strings.ReplaceAll(temporaryPlanSource, "root =", "import os as module\nmodule.environ['MEKUGI_BATCH_PLAN_DIR'] = '.'\nroot ="), unknown: true},
		{name: "environment imported alias mutation", setup: temporaryPlanSetup, source: strings.ReplaceAll(temporaryPlanSource, "root =", "from os import environ as environment\nenvironment['MEKUGI_BATCH_PLAN_DIR'] = '.'\nroot ="), unknown: true},
		{name: "module escapes as argument", setup: temporaryPlanSetup, source: strings.ReplaceAll(temporaryPlanSource, "root =", "update_environment(os)\nroot ="), unknown: true},
		{name: "rebound Path import alias", setup: temporaryPlanSetup, source: "import os\nfrom pathlib import Path as P\nP = get_path_constructor()\nP(os.environ['MEKUGI_BATCH_PLAN_DIR']).write_text('code')\n", unknown: true},
		{name: "Path absolute segment override", setup: temporaryPlanSetup, source: "import os\nfrom pathlib import Path\nPath(os.environ['MEKUGI_BATCH_PLAN_DIR'], '/workspace/code.go').write_text('code')\n", unknown: true},
		{name: "Path relative segment", setup: temporaryPlanSetup, source: "import os\nfrom pathlib import Path\nPath(os.environ['MEKUGI_BATCH_PLAN_DIR'], 'code.go').write_text('code')\n", unknown: true},
		{name: "Path dynamic segment", setup: temporaryPlanSetup, source: "import os\nfrom pathlib import Path\nPath(os.environ['MEKUGI_BATCH_PLAN_DIR'], dynamic).write_text('code')\n", unknown: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			command := temporaryPlanCommand(tc.setup, tc.source)
			blocks := toolOperationBlocks(toolActivityShell(command))
			var unknown, workspace, run bool
			for _, block := range blocks {
				unknown = unknown || block.Verb == "Edit" && strings.HasPrefix(block.Label, "paths unavailable")
				workspace = workspace || block.Verb == "Edit" && strings.Contains(block.Label, "workspace.go")
				run = run || block.Verb == "Run"
				if strings.ContainsRune(block.Label, '\x00') {
					t.Fatalf("symbolic temporary path escaped into activity: %+v", block)
				}
			}
			if unknown != tc.unknown || workspace != tc.workspace || (!tc.unknown && !tc.workspace && !run) {
				t.Fatalf("unknown=%v workspace=%v run=%v; blocks=%+v", unknown, workspace, run, blocks)
			}
		})
	}
}

func TestTemporaryWritePreservesCaptureUncertainty(t *testing.T) {
	command := temporaryPlanCommand(temporaryPlanSetup+"gh run view 37104437662 --log-failed > \"$batch_plan_dir/ci-failure.log\"\n", temporaryPlanSource)
	plan := classifyExecShell(command, t.TempDir(), "bash")
	if plan.Class != execOpaque || len(plan.Scope) != 0 {
		t.Fatalf("display-only proof changed capture coverage: %+v", plan)
	}
}

func TestUISnapshotTemporaryWriteActivity(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	for _, agent := range []string{"Main", "/root/worker"} {
		for _, unresolved := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/unknown=%v", agent, unresolved), func(t *testing.T) {
				source := temporaryPlanSource
				if unresolved {
					source += "Path(dynamic).write_text('code')\n"
				}
				command := temporaryPlanCommand(temporaryPlanSetup, source)
				v := newLiveActivityView()
				v.childrenOnly = agent != "Main"
				v.clock = func() time.Time { return now }
				v.painter.Theme = livediff.DarkTheme
				output := new(activityui.Retention).New()
				output.Finish(new("/tmp/mekugi-batches.YcIbga/plan.json\n"), new(0))
				v.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{
					Seq: 1, Agent: agent, Kind: "tool", CallID: "plan", Text: toolActivityShell(command), Observed: now,
					outputTail: []string{"/tmp/mekugi-batches.YcIbga/plan.json"},
					native:     &liveActivityNativeItem{command: command, duration: 157 * time.Millisecond, output: output},
				}}})
				feed := v.renderFeed(100, 80)
				if agent == "Main" {
					feed = v.renderConversation(100)
				}
				assertNativeUISnapshot(t, fmt.Sprintf("temporary-write-%s-unknown-%v", strings.TrimPrefix(agent, "/root/"), unresolved), feed.lines)
			})
		}
	}
}

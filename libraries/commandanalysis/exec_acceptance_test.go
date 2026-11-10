package commandanalysis

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestCommandLegacyEncodingAndIdentity(t *testing.T) {
	for _, test := range []struct {
		command Command
		encoded string
	}{
		{Command{Command: "pwd", Workdir: "/work"}, `{"Command":"pwd","Workdir":"/work"}`},
		{Command{Command: "pwd", Workdir: "/work", Shell: "bash"}, `{"Command":"pwd","Workdir":"/work","Shell":"bash"}`},
	} {
		encoded, err := json.Marshal(test.command)
		if err != nil || string(encoded) != test.encoded {
			t.Fatalf("Marshal(%+v) = %s, %v; want %s", test.command, encoded, err, test.encoded)
		}
		var restored Command
		if err := json.Unmarshal(encoded, &restored); err != nil || restored != test.command {
			t.Fatalf("round trip = %+v, %v; want %+v", restored, err, test.command)
		}
	}
}

func TestLiteralExecCommandsRetainsPartialObservation(t *testing.T) {
	source := `await tools.exec_command({cmd:"first", unknown:{nested:[null,true,1]}});
await tools.exec_command({cmd:"unresolved", unknown:runtimeValue});
await tools.exec_command({cmd:"last", workdir:"child", shell:"sh"});`
	got, dynamic := LiteralExecCommands(source, "/work", "bash")
	want := []Command{{"first", "/work", "bash"}, {"last", "/work/child", "sh"}}
	if !dynamic || !slices.Equal(got, want) {
		t.Fatalf("LiteralExecCommands = %+v, dynamic=%v; want %+v, dynamic=true", got, dynamic, want)
	}
}

func TestLiteralExecCommandsLegacyFields(t *testing.T) {
	for _, test := range []struct {
		name, object string
		want         []Command
		dynamic      bool
	}{
		{"null environment", `{cmd:"pwd", environment_id:null}`, nil, true},
		{"null optional fields", `{cmd:"pwd", workdir:null, shell:null}`, nil, true},
		{"case sensitive", `{CMD:"pwd", WORKDIR:"child", SHELL:"sh"}`, nil, true},
		{"literal duplicate", `{cmd:"first", cmd:"last"}`, []Command{{"last", "/work", "bash"}}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, dynamic := LiteralExecCommands("await tools.exec_command("+test.object+");", "/work", "bash")
			if dynamic != test.dynamic || !slices.Equal(got, test.want) {
				t.Fatalf("LiteralExecCommands = %+v, dynamic=%v; want %+v, dynamic=%v", got, dynamic, test.want, test.dynamic)
			}
		})
	}
}

func TestLiteralExecCommandsObservationBounds(t *testing.T) {
	const call = `await tools.exec_command({cmd:"pwd"});`
	for _, count := range []int{32, 33} {
		commands, dynamic := LiteralExecCommands(strings.Repeat(call, count), "/work", "bash")
		if count == 32 && (dynamic || len(commands) != 32) || count == 33 && (!dynamic || commands != nil) {
			t.Errorf("%d commands: got %d, dynamic=%v", count, len(commands), dynamic)
		}
	}
	boundary := call + strings.Repeat(" ", MaxScriptBytes-len(call))
	commands, dynamic := LiteralExecCommands(boundary, "/work", "bash")
	if dynamic || len(commands) != 1 {
		t.Fatalf("exact byte bound: got %+v, dynamic=%v", commands, dynamic)
	}
	commands, dynamic = LiteralExecCommands(boundary+" ", "/work", "bash")
	if commands != nil || dynamic {
		t.Fatalf("over byte bound: got %+v, dynamic=%v; want nil, false", commands, dynamic)
	}
}

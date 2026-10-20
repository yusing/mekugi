package commandanalysis

import "testing"

func TestStockLiteralExecCommands(t *testing.T) {
	directory := "/work"
	commands, dynamic := LiteralExecCommands(`await tools.exec_command({cmd: "rm a", workdir: "sub"}); text(await tools.exec_command({cmd: 'cp b c'}));`, directory, "bash")
	if dynamic || len(commands) != 2 || commands[0].Command != "rm a" || commands[0].Workdir != "/work/sub" || commands[1].Workdir != directory {
		t.Fatalf("literal commands = %+v dynamic=%v", commands, dynamic)
	}
	for _, source := range []string{
		`const cmd = "rm a"; await tools.exec_command({cmd});`,
		`await tools.exec_command({cmd: name});`,
		`const run = tools.exec_command; await run({cmd: "rm a"});`,
		`const t = tools; await t.exec_command({cmd: "rm a"});`,
		`await tools["exec_command"]({cmd: "rm a"});`,
		`await tools.exec_command({cmd: "cat", tty: true}); await tools.write_stdin({session_id: 1, chars: "x"});`,
		`await tools?.exec_command({cmd: "rm a"});`,
		`await globalThis.tools.exec_command({cmd: "rm a"});`,
		`await eval("tools").exec_command({cmd: "rm a"});`,
		`await tools.exec_command({cmd: "rm a", environment_id: "remote"});`,
	} {
		if _, dynamic := LiteralExecCommands(source, directory, "bash"); !dynamic {
			t.Errorf("%s was treated as literal", source)
		}
	}
	for _, source := range []string{
		`await tools.write_stdin({session_id: 1});`,
		`await tools.write_stdin({session_id: 1, chars: ""});`,
		`const session_id = 1; await tools.write_stdin({session_id, chars: ""});`,
	} {
		if commands, dynamic := LiteralExecCommands(source, directory, "bash"); dynamic || len(commands) != 0 {
			t.Errorf("poll-only write_stdin call %s = %+v dynamic=%v; want no command and static", source, commands, dynamic)
		}
	}
	for _, source := range []string{
		`await tools.write_stdin({session_id: 1, chars: "x"});`,
		`await tools.write_stdin({session_id: 1, chars: input});`,
		`await tools.write_stdin({session_id: 1, ...options, chars: ""});`,
		`await tools.write_stdin({session_id: 1, ["chars"]: ""});`,
		`await tools.write_stdin({session_id: 1, chars: "", __proto__: {}});`,
		`await tools.write_stdin({session_id: 1, chars: "", chars: ""});`,
	} {
		if _, dynamic := LiteralExecCommands(source, directory, "bash"); !dynamic {
			t.Errorf("ambiguous write_stdin call %s was treated as a poll", source)
		}
	}
}

func TestExecCommandArguments(t *testing.T) {
	if _, ok := ExecCommandArguments(`{"cmd":"rm a","environment_id":"remote"}`, "/work", "bash"); ok {
		t.Error("a command for another environment was observed")
	}
	if command, ok := ExecCommandArguments(`{"cmd":"rm a"}`, "/work", "bash"); !ok || command.Shell != "bash" {
		t.Errorf("session shell was not applied: %+v", command)
	}
}

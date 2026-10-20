package sourceparse

import (
	"slices"
	"testing"
)

func TestJavaScriptCallRecognizers(t *testing.T) {
	for _, test := range []struct {
		source string
		path   []string
		args   []string
		match  bool
	}{
		{`tools /* owner */ . exec_command(/* before */ {cmd:"pwd"}, /* between */ 2 /* after */)`, []string{"tools", "exec_command"}, []string{`{cmd:"pwd"}`, "2"}, true},
		{`tools.exec_command(/* empty */)`, []string{"tools", "exec_command"}, nil, true},
		{`tools.exec_command()`, []string{"tools", "write_stdin"}, nil, false},
		{`tools?.exec_command()`, []string{"tools", "exec_command"}, nil, false},
		{`tools.exec_command?.()`, []string{"tools", "exec_command"}, nil, false},
		{`tools["exec_command"]()`, []string{"tools", "exec_command"}, nil, false},
		{`globalThis.tools.exec_command(1)`, []string{"globalThis", "tools", "exec_command"}, []string{"1"}, true},
		{`globalThis?.tools.exec_command()`, []string{"globalThis", "tools", "exec_command"}, nil, false},
		{`exec_command(1)`, []string{"exec_command"}, []string{"1"}, true},
		{`tools.exec_command()`, nil, nil, false},
	} {
		t.Run(test.source, func(t *testing.T) {
			source := []byte(test.source)
			tree, err := Parse(source, JavaScript, nil)
			if err != nil || tree == nil {
				t.Fatalf("Parse = %v, %v", tree, err)
			}
			defer tree.Close()
			if tree.RootNode().HasError() {
				t.Fatal("fixture contains a syntax error")
			}
			call := tree.RootNode().NamedChild(0).NamedChild(0)
			args, ok := JavaScriptCallArguments(call, source, test.path...)
			var got []string
			for _, arg := range args {
				got = append(got, arg.Utf8Text(source))
			}
			if ok != test.match || !slices.Equal(got, test.args) {
				t.Errorf("arguments = %q, %v; want %q, %v", got, ok, test.args, test.match)
			}
			// Optional invocation changes the call, not its ordinary member path.
			wantMember := test.match || test.source == `tools.exec_command?.()`
			if got := JavaScriptMemberPath(call.ChildByFieldName("function"), source, test.path...); got != wantMember {
				t.Errorf("member path = %v; want %v", got, wantMember)
			}
			if _, ok := JavaScriptCallArguments(tree.RootNode(), source, test.path...); ok {
				t.Error("program recognized as a call")
			}
		})
	}
	if _, ok := JavaScriptCallArguments(nil, nil, "tools"); ok || JavaScriptMemberPath(nil, nil, "tools") {
		t.Fatal("nil node recognized")
	}
}

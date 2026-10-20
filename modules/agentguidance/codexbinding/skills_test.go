package codexbinding

import "testing"

func TestRewriteSelectedSkillInstructions(t *testing.T) {
	selected := "<skill>\n<name>golang-best-practices</name>\n<path>/home/user/.agents/skills/golang-best-practices/SKILL.md</path>\n---\nname: golang-best-practices\n---\n\n</skill>"
	want := `<skill name="golang-best-practices"/>`
	for _, test := range []struct{ name, input, want string }{
		{"selected skill", selected, want},
		{"surrounding whitespace", " \n" + selected + "\n ", " \n" + want + "\n "},
		{"resource access collapsed", "<skill>\n<name>remote</name>\n<path>skill://remote/SKILL.md</path>\n<resource_access>{}</resource_access>\nbody\n</skill>", `<skill name="remote"/>`},
		{"name escaped", "<skill>\n<name>a & \"b\"</name>\n<path>/tmp/SKILL.md</path>\nbody\n</skill>", `<skill name="a &amp; &#34;b&#34;"/>`},
		{"ordinary user text", "Please quote " + selected, "Please quote " + selected},
		{"missing name", "<skill>\n<path>/tmp/SKILL.md</path>\nbody\n</skill>", "<skill>\n<path>/tmp/SKILL.md</path>\nbody\n</skill>"},
		{"path in body", "<skill>\n<name>demo</name>\nbody <path>keep</path>\n</skill>", "<skill>\n<name>demo</name>\nbody <path>keep</path>\n</skill>"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := rewriteSelectedSkillInstructions(test.input); got != test.want {
				t.Fatalf("got %q, want %q", got, test.want)
			}
		})
	}
}

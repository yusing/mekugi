python3 - <<'PY'
from pathlib import Path
p=Path('go.mod'); p.write_text(p.read_text().replace('go 1.26.0','go 1.27.0'))
p=Path('README.md'); s=p.read_text().replace('Go 1.26+', 'Go 1.27+').replace('The helper uses the\ninstalled binary', 'The helper is the same Go executable installed under its Git command name and')
p.write_text(s)
p=Path('api.go'); s=p.read_text().replace('"encoding/json"','"encoding/json/jsontext"\n "encoding/json/v2"').replace('json.RawMessage','jsontext.Value'); p.write_text(s)
p=Path('git.go'); s=p.read_text().replace('"context"','"cmp"\n "context"').replace('"sort"','"maps"\n "slices"')
a=s.index('func sortedKeys['); b=s.index('\nfunc pushedCommits',a); s=s[:a]+s[b:]
s=s.replace('sortedKeys(heads)','slices.Sorted(maps.Keys(heads))').replace('sortedKeys(after)','slices.Sorted(maps.Keys(after))').replace('sortedKeys(before)','slices.Sorted(maps.Keys(before))')
a=s.index('\tfound := false'); b=s.index('\toutput, err :=',a)
s=s[:a]+'''\tif !slices.Contains(strings.Split(remotes, "\\n"), remote) {
        return nil, errors.New("Selected Git remote is not configured.")
    }
'''+s[b:]
a=s.index('\tsort.Slice('); b=s.index('\treturn commits, nil',a)
s=s[:a]+'''\tslices.SortFunc(commits, func(a, b commit) int {
        return cmp.Or(a.authored.Compare(b.authored), cmp.Compare(a.sha, b.sha))
    })
'''+s[b:]
s=s.replace('range branches {','range maps.Values(branches) {') if False else s
s=s.replace('for _, sha := range branches {','for sha := range maps.Values(branches) {')
for expr in ['output','remotes']:
 s=s.replace(f'range strings.Split({expr}, "\\n")',f'range strings.SplitSeq({expr}, "\\n")')
p.write_text(s)
p=Path('sync.go'); s=p.read_text().replace('"encoding/json"','"encoding/json/v2"').replace('"sort"','"maps"\n "slices"')
s=s.replace('sortedKeys(form.Embedded.ValidationErrors)','slices.Sorted(maps.Keys(form.Embedded.ValidationErrors))')
s=s.replace('''\t\t\tsubject := strings.Map(func(r rune) rune { return r }, item.subject)
''','')
s=s.replace('for _, r := range subject {','for _, r := range item.subject {')
a=s.index('\tids := make([]int, 0, len(tasks))'); b=s.index('\tvar operations',a)
s=s[:a]+'\tids := slices.Sorted(maps.Keys(tasks))\n'+s[b:]
p.write_text(s)
PY
gofmt -w main.go api.go git.go sync.go && make build && make test

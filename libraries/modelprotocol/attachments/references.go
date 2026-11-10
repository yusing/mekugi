package attachments

import (
	json "encoding/json/v2"
	"fmt"
	"iter"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxDepth includes the explicitly selected attachment.
const MaxDepth = 3

// Reference is the parsed identity of one reference string. Paths and skills
// are not authority: acquisition remains with the receiving source reader.
type Reference struct {
	Path, Skill string
	Ranges      [][2]int
	Changes     []string
}

// ReadPath resolves an internally supplied native skill anchor. Public skill
// references have no path; their acquisition owner supplies this metadata.
func (r Reference) ReadPath() string {
	if _, reference, ok := strings.Cut(r.Skill, "/"); ok && filepath.Base(r.Path) == "SKILL.md" {
		return filepath.Join(filepath.Dir(r.Path), reference)
	}
	return r.Path
}

// ParseReference is shared by explicit selections and references in selected
// text. Captured changes use mchanges:ID[,ID..ID]; these are reader arguments,
// never shell source. Native skill paths are resolved internally by readers.
func ParseReference(text string) (Reference, error) {
	fail := func() (Reference, error) { return Reference{}, fmt.Errorf("invalid source reference %.256q", text) }
	if strings.ContainsAny(text, "\x00\r\n") {
		return fail()
	}
	if changes, ok := strings.CutPrefix(text, "mchanges:"); ok {
		refs := strings.Split(changes, ",")
		for _, ref := range refs {
			first, last, pair := strings.Cut(ref, "..")
			if !changeID(first, false) || pair && !changeID(last, true) {
				return fail()
			}
		}
		return Reference{Changes: refs}, nil
	}
	if skill, ok := strings.CutPrefix(text, "$"); ok {
		var suffix string
		if at := strings.LastIndex(skill, "#L"); at >= 0 {
			skill, suffix = skill[:at], skill[at:]
		}
		if !ValidSkillReference(skill) {
			return fail()
		}
		ranges, err := referenceRanges(suffix)
		if err != nil {
			return fail()
		}
		return Reference{Skill: skill, Ranges: ranges}, nil
	}
	path, ok := strings.CutPrefix(text, "@")
	if !ok || path == "" || strings.HasPrefix(path, "@") {
		return fail()
	}
	var suffix string
	if path[0] == '\'' || path[0] == '"' {
		decoded, end, err := quotedPath(path)
		if err != nil {
			return fail()
		}
		path, suffix = decoded, path[end:]
	} else if at := strings.LastIndex(path, "#L"); at >= 0 {
		path, suffix = path[:at], path[at:]
	}
	if path == "" || len(path) > 4096 || strings.ContainsAny(path, "\x00\r\n") {
		return fail()
	}
	ranges, err := referenceRanges(suffix)
	if err != nil {
		return fail()
	}
	return Reference{Path: path, Ranges: ranges}, nil
}

func quotedPath(text string) (string, int, error) {
	if text[0] == '\'' {
		if at := strings.IndexByte(text[1:], '\''); at >= 0 {
			return text[1 : at+1], at + 2, nil
		}
		return "", 0, fmt.Errorf("unclosed path quote")
	}
	quoted, err := strconv.QuotedPrefix(text)
	var path string
	if err == nil {
		err = json.Unmarshal([]byte(quoted), &path)
	}
	return path, len(quoted), err
}

func referenceRanges(suffix string) ([][2]int, error) {
	if suffix == "" {
		return nil, nil
	}
	suffix, ok := strings.CutPrefix(suffix, "#")
	if !ok {
		return nil, fmt.Errorf("invalid line ranges")
	}
	var ranges [][2]int
	for span := range strings.SplitSeq(suffix, ",") {
		span, ok := strings.CutPrefix(span, "L")
		first, last, pair := strings.Cut(span, "-L")
		if !pair {
			last = first
		}
		start, startErr := strconv.Atoi(first)
		end, endErr := strconv.Atoi(last)
		if !ok || startErr != nil || endErr != nil || start < 1 || end < start || strconv.Itoa(start) != first || strconv.Itoa(end) != last {
			return nil, fmt.Errorf("invalid line ranges")
		}
		ranges = append(ranges, [2]int{start, end})
	}
	return ranges, nil
}

// The retained-change owner validates actual handle words and chain identity.
// Here only recognize the command's ID/range syntax, never options or paths.
func changeID(id string, abbreviated bool) bool {
	at := 0
	for at < len(id) && id[at] >= 'a' && id[at] <= 'z' {
		at++
	}
	if at == 0 && !abbreviated || at == len(id) || id[at] < '1' || id[at] > '9' {
		return false
	}
	for _, digit := range id[at:] {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
}

// String is the canonical spelling used when resolved internal identities
// become public selections again. It never embeds a native skill path.
func (r Reference) String() string {
	if r.Changes != nil {
		return "mchanges:" + strings.Join(r.Changes, ",")
	}
	text := "$" + r.Skill
	if r.Skill == "" {
		path := r.Path
		if strings.ContainsAny(path, " \t\"'`()[]{}<>,;") || strings.HasPrefix(path, "@") || strings.Contains(path, "#L") {
			if !strings.ContainsRune(path, '\'') {
				path = "'" + path + "'"
			} else {
				path = strconv.Quote(path)
			}
		}
		text = "@" + path
	}
	for i, span := range r.Ranges {
		if i == 0 {
			text += "#"
		} else {
			text += ","
		}
		text += fmt.Sprintf("L%d-L%d", span[0], span[1])
	}
	return text
}

func ValidSkillReference(skill string) bool {
	if skill == "" || len(skill) > 256 || strings.HasPrefix(skill, "-") || strings.ContainsAny(skill, "\x00\r\n\t \\\"'`$#()[]{}<>,;") {
		return false
	}
	for part := range strings.SplitSeq(skill, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

// ReferenceToken preserves the source spelling and its byte span for composer
// bindings. Malformed candidates remain tokens so readers can report failures.
type ReferenceToken struct {
	Text       string
	Start, End int
}

func References(text string) iter.Seq[string] {
	return func(yield func(string) bool) {
		for token := range ReferenceTokens(text) {
			if !yield(token.Text) {
				return
			}
		}
	}
}

// ReferenceTokens recognizes the same language in prose and JSON values.
// Escaped markers, email addresses, identifier suffixes and diff markers stay
// literal. Quoting after @ permits spaces or a leading @ in a path.
func ReferenceTokens(text string) iter.Seq[ReferenceToken] {
	return func(yield func(ReferenceToken) bool) {
		for i := 0; i < len(text); i++ {
			marker := text[i]
			if marker != '@' && marker != '$' && !strings.HasPrefix(text[i:], "mchanges:") {
				continue
			}
			if i > 0 {
				previous, _ := utf8.DecodeLastRuneInString(text[:i])
				if !unicode.IsSpace(previous) && !strings.ContainsRune("\"'`([{:", previous) {
					continue
				}
				// A selected JSON string carries one complete reference, including
				// its escaped quotes. Decode before interpreting the reference.
				if text[i-1] == '"' {
					if decoded, size, err := quotedPath(text[i-1:]); err == nil {
						if _, err := ParseReference(decoded); err == nil {
							end := i + size - 2
							if !yield(ReferenceToken{decoded, i, end}) {
								return
							}
							i = end
							continue
						}
					}
				}
			}
			start, end := i, i+1
			if marker != '@' && marker != '$' {
				end = i + len("mchanges:")
			}
			if end == len(text) || marker == '@' && text[end] == '@' || marker == '$' && strings.ContainsRune("${(", rune(text[end])) {
				continue
			}
			if marker == '@' && (text[end] == '\'' || text[end] == '"') {
				_, size, err := quotedPath(text[end:])
				if err != nil {
					continue
				}
				end += size
				if !strings.HasPrefix(text[end:], "#L") {
					i = end - 1
					if !yield(ReferenceToken{text[start:end], start, end}) {
						return
					}
					continue
				}
			}
			for end < len(text) {
				r, size := utf8.DecodeRuneInString(text[end:])
				if unicode.IsSpace(r) || strings.ContainsRune("\"'`()[]{}<>;", r) || r == ',' && marker != 'm' && !strings.Contains(text[start:end], "#L") {
					break
				}
				end += size
			}
			i = end - 1
			if end > start+1 && !yield(ReferenceToken{text[start:end], start, end}) {
				return
			}
		}
	}
}

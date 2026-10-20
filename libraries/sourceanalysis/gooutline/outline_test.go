package gooutline

import (
	"strings"
	"testing"
)

func TestParseKeepsFullDeclarationBodyDespiteUnrelatedSyntaxError(t *testing.T) {
	source := "package p\n" +
		"func Complete() {\n" +
		"\tif true {\n" +
		"\t\t_ = \"a closing brace } inside a string\"\n" +
		"\t}\n" +
		"}\n" +
		"var broken =\n"

	document := Parse(source)
	if len(document.Errors) == 0 {
		t.Fatal("Parse did not report the unrelated malformed declaration")
	}
	entry := outlineEntry(t, document, "function", "Complete")
	const declaration = "func Complete() {\n\tif true {\n\t\t_ = \"a closing brace } inside a string\"\n\t}\n}"
	if got := source[entry.From:entry.To]; got != declaration {
		t.Fatalf("declaration range = %q, want full body %q", got, declaration)
	}
	if !entry.Complete {
		t.Fatalf("unrelated syntax error marked Complete declaration incomplete: %+v; diagnostics=%+v", entry, document.Errors)
	}
}

func TestParseMarksMalformedDeclarationIncomplete(t *testing.T) {
	source := "package p\n" +
		"func Broken() {\n" +
		"\tvalue :=\n" +
		"}\n"
	document := Parse(source)
	if len(document.Errors) == 0 {
		t.Fatal("Parse did not report the malformed function body")
	}
	broken := outlineEntry(t, document, "function", "Broken")
	if broken.Complete {
		t.Fatalf("malformed declaration reported complete: %+v; diagnostics=%+v", broken, document.Errors)
	}
}

func TestParseMalformedHeaderHasNoCompleteRange(t *testing.T) {
	source := "package p\nfunc Malformed( {\n"
	document := Parse(source)
	entry := outlineEntry(t, document, "function", "Malformed")
	if entry.Complete || entry.From > entry.To || entry.To > len(source) {
		t.Fatalf("malformed header boundary: %+v", entry)
	}
}

func TestParseUsesUTF8ByteOffsetsAndDecodesImports(t *testing.T) {
	source := "package p\n" +
		"// café 雪 precedes the imports\n" +
		"import (\n" +
		"\t\"fmt\"\n" +
		"\talias \"example.org/pkg\"\n" +
		")\n" +
		"func afterImports() {}\n"
	document := Parse(source)
	if len(document.Errors) != 0 {
		t.Fatalf("valid source produced diagnostics: %+v", document.Errors)
	}

	fmtImport := outlineEntry(t, document, "import", "fmt")
	checkByteSpan(t, source, fmtImport, "\"fmt\"")
	aliasedImport := outlineEntry(t, document, "import", "example.org/pkg")
	checkByteSpan(t, source, aliasedImport, "alias \"example.org/pkg\"")
	for _, entry := range []Entry{fmtImport, aliasedImport} {
		if entry.NameFrom != -1 || entry.NameTo != -1 {
			t.Errorf("import path entry has name-token offsets, want -1 sentinel: %+v", entry)
		}
	}
	after := outlineEntry(t, document, "function", "afterImports")
	checkByteSpan(t, source, after, "func afterImports() {}")
	nameFrom := strings.Index(source, "afterImports")
	if after.NameFrom != nameFrom || after.NameTo != nameFrom+len("afterImports") || source[after.NameFrom:after.NameTo] != "afterImports" {
		t.Fatalf("function name byte range = %d:%d, want %d:%d", after.NameFrom, after.NameTo, nameFrom, nameFrom+len("afterImports"))
	}
}

func TestParseHandlesGroupedConstTypesAndGenericMethods(t *testing.T) {
	source := "package p\n" +
		"const (\n" +
		"\tFirst = iota\n" +
		"\tSecond\n" +
		"\tPairA, PairB = 3, 4\n" +
		")\n" +
		"type (\n" +
		"\tStore[T any] struct { value T }\n" +
		"\tKey = string\n" +
		")\n" +
		"func (store *Store[T]) Convert[U any](value U) U {\n" +
		"\treturn value\n" +
		"}\n"
	document := Parse(source)
	if len(document.Errors) != 0 {
		t.Fatalf("Go 1.27 generic method source produced diagnostics: %+v", document.Errors)
	}

	for _, name := range []string{"First", "Second", "PairA", "PairB"} {
		entry := outlineEntry(t, document, "constant", name)
		if !entry.Complete {
			t.Errorf("grouped constant %s is incomplete: %+v", name, entry)
		}
	}
	for _, name := range []string{"Store", "Key"} {
		entry := outlineEntry(t, document, "type", name)
		if !entry.Complete {
			t.Errorf("grouped type %s is incomplete: %+v", name, entry)
		}
	}
	for _, name := range []string{"PairA", "PairB"} {
		entry := outlineEntry(t, document, "constant", name)
		checkByteSpan(t, source, entry, "PairA, PairB = 3, 4")
	}
	store := outlineEntry(t, document, "type", "Store")
	checkByteSpan(t, source, store, "Store[T any] struct { value T }")
	key := outlineEntry(t, document, "type", "Key")
	checkByteSpan(t, source, key, "Key = string")
	method := outlineEntry(t, document, "method", "Convert")
	if method.Receiver != "*Store[T]" {
		t.Fatalf("generic method receiver = %q, want %q", method.Receiver, "*Store[T]")
	}
	checkByteSpan(t, source, method, "func (store *Store[T]) Convert[U any](value U) U {\n\treturn value\n}")
	if source[method.NameFrom:method.NameTo] != "Convert" {
		t.Fatalf("generic method name offsets select %q", source[method.NameFrom:method.NameTo])
	}
}

func outlineEntry(t *testing.T, document Document, kind, name string) Entry {
	t.Helper()
	var matches []Entry
	for _, entry := range document.Entries {
		if entry.Kind == kind && entry.Name == name {
			matches = append(matches, entry)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("found %d %s entries named %q, want exactly one; document=%+v", len(matches), kind, name, document)
	}
	return matches[0]
}

func checkByteSpan(t *testing.T, source string, entry Entry, want string) {
	t.Helper()
	if entry.From < 0 || entry.To > len(source) || entry.From > entry.To {
		t.Fatalf("invalid byte offsets %d:%d for %d-byte source", entry.From, entry.To, len(source))
	}
	if got := source[entry.From:entry.To]; got != want {
		t.Fatalf("byte range %d:%d selects %q, want %q", entry.From, entry.To, got, want)
	}
}

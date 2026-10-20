package toolplugin

import (
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestNativeInspectRustDeclarationsAndSpans(t *testing.T) {
	source := strings.Join([]string{
		"#![allow(dead_code)]", "use std::{", "    fmt::{self, Display as Show},", "    io::*,", "};", "extern crate alloc as heap;",
		"#[repr(C)]", "pub struct Boxed<T> {", "    hidden: T,", "}", "pub struct Pair(i32, i32);", "pub struct Unit;",
		"enum Choice {", "    First { hidden: i32 },", "    Second,", "}", "union Bits { hidden: u32 }", "type Alias<T> = Boxed<T>;",
		"const VALUE: u8 = {", "    const hidden: u8 = 1;", "    hidden", "};", "static mut GLOBAL: u8 = 9;",
		"pub trait Service<T> {", "    type Output;", "    const LIMIT: usize;", "    #[must_use]", "    fn required(", "        &self,", "        input: T,", "    ) -> Self::Output;",
		"    fn defaulted(&self) {", "        fn hidden() {}", "    }", "}",
		"impl<T> Service<T> for crate::Boxed<T> {", "    type Output = T;", "    const LIMIT: usize = 1;", "    #[inline]", "    fn required(&self, input: T) -> T {", "        fn hidden() {}", "        input", "    }", "}",
		"impl<T> Boxed<T> {", "    pub const fn new(input: T) -> Self {", "        Self { hidden: input }", "    }", "    pub async unsafe fn r#match(&self) {}", "}",
		"unsafe extern \"C\" {", "    pub fn foreign(", "        input: i32,", "    ) -> i32;", "    static FOREIGN: i32;", "}",
		"mod child { pub fn hidden() {} }", "pub mod remote;", "macro_rules! make {", "    () => { fn hidden() {} };", "}", "make!();",
		"#[cfg(any())]", "pub async fn visible() {", "    fn hidden() {}", "}",
	}, "\n")
	want := []nativeOutlineEntry{
		{Kind: "import", Name: "fmt", Line: 2, LineEnd: 5}, {Kind: "import", Name: "Show", Line: 2, LineEnd: 5}, {Kind: "import", Name: "*", Line: 2, LineEnd: 5},
		{Kind: "import", Name: "heap", Line: 6, LineEnd: 6},
		{Kind: "type", Name: "Boxed", Line: 7, LineEnd: 10}, {Kind: "type", Name: "Pair", Line: 11, LineEnd: 11}, {Kind: "type", Name: "Unit", Line: 12, LineEnd: 12},
		{Kind: "type", Name: "Choice", Line: 13, LineEnd: 16}, {Kind: "type", Name: "Bits", Line: 17, LineEnd: 17}, {Kind: "type", Name: "Alias", Line: 18, LineEnd: 18},
		{Kind: "constant", Name: "VALUE", Line: 19, LineEnd: 22}, {Kind: "variable", Name: "GLOBAL", Line: 23, LineEnd: 23},
		{Kind: "type", Name: "Service", Line: 24, LineEnd: 35}, {Kind: "method", Name: "required", Receiver: "Service", Line: 27, LineEnd: 31}, {Kind: "method", Name: "defaulted", Receiver: "Service", Line: 32, LineEnd: 34},
		{Kind: "method", Name: "required", Receiver: "crate::Boxed", Line: 39, LineEnd: 43}, {Kind: "method", Name: "new", Receiver: "Boxed", Line: 46, LineEnd: 48}, {Kind: "method", Name: "r#match", Receiver: "Boxed", Line: 49, LineEnd: 49},
		{Kind: "function", Name: "foreign", Line: 52, LineEnd: 54}, {Kind: "variable", Name: "FOREIGN", Line: 55, LineEnd: 55},
		{Kind: "module", Name: "child", Line: 57, LineEnd: 57}, {Kind: "module", Name: "remote", Line: 58, LineEnd: 58}, {Kind: "macro", Name: "make", Line: 59, LineEnd: 61}, {Kind: "function", Name: "visible", Line: 63, LineEnd: 66},
	}
	wantCompact := "2-6 import\n7-10 type Boxed\n11-11 type Pair\n12-12 type Unit\n13-16 type Choice\n17-17 type Bits\n18-18 type Alias\n19-22 constant VALUE\n23-23 variable GLOBAL\n24-35 type Service\n27-31 method Service.required\n32-34 method Service.defaulted\n39-43 method crate::Boxed.required\n46-48 method Boxed.new\n49-49 method Boxed.r#match\n52-54 function foreign\n55-55 variable FOREIGN\n57-57 module child\n58-58 module remote\n59-61 macro make\n63-66 function visible\n"
	for _, separator := range []string{"\n", "\r\n", "\r"} {
		t.Run(fmt.Sprintf("separator_%q", separator), func(t *testing.T) {
			text := "\ufeff" + strings.ReplaceAll(source, "\n", separator) + separator
			path := nativeFixture(t, "declarations.rs", text)
			result := nativeInspectFixture(t, path)
			if result.Data.Kind != "code" || result.Data.Language == nil || *result.Data.Language != "rust" || result.Data.SizeBytes != len(text) || result.Data.LineCount == nil || *result.Data.LineCount != 66 || !result.Data.ParseComplete || !reflect.DeepEqual(result.Data.Outline, want) {
				t.Fatalf("Rust projection: %+v, want %+v", result.Data, want)
			}
			out := nativeExecute(t, "inspect_file", path)
			if out.ExitCode != 0 || out.Stderr != "" || out.Stdout != wantCompact {
				t.Fatalf("compact Rust outline: %+v, want %q", out, wantCompact)
			}
		})
	}
}

func TestNativeInspectRustReceiversAndImportBindings(t *testing.T) {
	source := "use std::fmt;\nuse std::{io::{self, Read}, fmt::Display as Show};\nextern crate alloc;\nimpl<const N: usize> Trait for Wrapper<{ const hidden: usize = 7; hidden }> { fn method() {} }\nimpl Trait for [u8; { const hidden: usize = 9; hidden }] { fn method() {} }\nimpl Trait for &'static mut crate::Type { fn method() {} }\nimpl Trait for *const Type { fn method() {} }\nimpl Trait for (Type, Other) { fn method() {} }\nimpl Trait for dyn Send + Sync { fn method() {} }\nimpl Trait for fn(u8) -> bool { fn method() {} }\nimpl Trait for <Type as Other>::Item { fn method() {} }\n"
	want := []nativeOutlineEntry{
		{Kind: "import", Name: "fmt", Line: 1, LineEnd: 1}, {Kind: "import", Name: "io", Line: 2, LineEnd: 2}, {Kind: "import", Name: "Read", Line: 2, LineEnd: 2}, {Kind: "import", Name: "Show", Line: 2, LineEnd: 2}, {Kind: "import", Name: "alloc", Line: 3, LineEnd: 3},
		{Kind: "method", Name: "method", Receiver: "Wrapper", Line: 4, LineEnd: 4}, {Kind: "method", Name: "method", Receiver: "[u8]", Line: 5, LineEnd: 5}, {Kind: "method", Name: "method", Receiver: "&crate::Type", Line: 6, LineEnd: 6}, {Kind: "method", Name: "method", Receiver: "*Type", Line: 7, LineEnd: 7}, {Kind: "method", Name: "method", Receiver: "(Type, Other)", Line: 8, LineEnd: 8}, {Kind: "method", Name: "method", Receiver: "Send + Sync", Line: 9, LineEnd: 9}, {Kind: "method", Name: "method", Receiver: "fn", Line: 10, LineEnd: 10}, {Kind: "method", Name: "method", Receiver: "<Type as Other>::Item", Line: 11, LineEnd: 11},
	}
	result := nativeInspectFixture(t, nativeFixture(t, "types.rs", source))
	if !result.Data.ParseComplete || !reflect.DeepEqual(result.Data.Outline, want) {
		t.Fatalf("Rust receiver/binding names: %+v, want %+v", result.Data, want)
	}
}

func TestNativeInspectRustSyntaxRecovery(t *testing.T) {
	for _, tc := range []struct {
		source, visible      string
		line, end, errorLine int
	}{
		{"fn complete() {\n}\nconst BROKEN: = ;\nfn recovered() {}\n", "recovered", 4, 4, 3},
		{"struct Good { field: u8 }\nimpl Good {\n fn broken(&self) { let value = ; }\n fn valid(&self) {}\n}\n", "valid", 4, 4, 3},
		{"fn valid() {}\nfn broken( {\n", "valid", 1, 1, 2},
		{"fn valid() {}\nfn broken() {\n", "valid", 1, 1, 2},
	} {
		t.Run(tc.visible+tc.source, func(t *testing.T) {
			path := nativeFixture(t, "broken.rs", tc.source)
			result := nativeInspectFixture(t, path)
			found, diagnostic := false, false
			for _, entry := range result.Data.Outline {
				if entry.Kind == "parse_error" {
					diagnostic = true
					if entry.Name != "syntax error" || entry.Line != tc.errorLine || entry.LineEnd != entry.Line {
						t.Fatalf("invalid diagnostic: %+v", entry)
					}
				}
				if entry.Name == tc.visible && entry.Line == tc.line && entry.LineEnd == tc.end {
					found = true
				}
			}
			if result.Data.ParseComplete || !found || !diagnostic {
				t.Fatalf("Rust recovery: %+v", result.Data)
			}
			for _, entry := range nativeParsedFixture(t, "broken.rs", tc.source).entries {
				if entry.name == tc.visible && !entry.complete {
					t.Fatalf("unrelated error marked declaration incomplete: %+v", entry)
				}
			}
		})
	}
}

func TestNativeInspectRustAttributesAndEmptyOutlines(t *testing.T) {
	source := "#[cfg(\n    any(),\n)]\n// attached comment\n#[inline]\npub(crate) unsafe extern \"C\" fn r#fn() {}\n"
	want := []nativeOutlineEntry{{Kind: "function", Name: "r#fn", Line: 1, LineEnd: 6}}
	result := nativeInspectFixture(t, nativeFixture(t, "attributes.rs", source))
	if !result.Data.ParseComplete || !reflect.DeepEqual(result.Data.Outline, want) {
		t.Fatalf("Rust attribute span: %+v, want %+v", result.Data, want)
	}
	for _, text := range []string{"", "// comment only\n", "#![allow(dead_code)]\n", "invoke! { fn hidden() {} }\n", "impl !Send for Type {}\n"} {
		path := nativeFixture(t, "empty.rs", text)
		result := nativeInspectFixture(t, path)
		out := nativeExecute(t, "inspect_file", path)
		if !result.Data.ParseComplete || len(result.Data.Outline) != 0 || out.ExitCode != 0 || out.Stdout != "(no outline)\n" || out.Stderr != "" {
			t.Fatalf("Rust no-outline source %q: %+v; %+v", text, result.Data, out)
		}
	}
}

func TestNativeInspectRustRecoveryDeclarationBoundaries(t *testing.T) {
	for _, source := range []string{
		"fn broken() {\n fn hidden() {}\n",
		"const X: usize = {\n fn hidden() {}\n",
		"fn visible() {}\nfn broken() {\n struct hidden;\n const hidden: u8 = 1;\n",
		"static X: usize = {\n trait hidden { fn method(); }\n",
		"fn broken() { let invalid = ;\n fn hidden() {}\n}\nfn visible() {}\n",
	} {
		t.Run(source, func(t *testing.T) {
			result := nativeInspectFixture(t, nativeFixture(t, "boundary.rs", source))
			if result.Data.ParseComplete {
				t.Fatal("expected incomplete parse")
			}
			visible := false
			for _, entry := range result.Data.Outline {
				if entry.Name == "hidden" || entry.Name == "method" {
					t.Fatalf("recovery exposed body-local declaration: %+v", entry)
				}
				visible = visible || entry.Name == "visible"
			}
			if strings.Contains(source, "fn visible()") && !visible {
				t.Fatalf("unrelated top-level function was lost: %+v", result.Data)
			}
		})
	}
}

func TestNativeInspectRustFailuresAndNoSemanticResolver(t *testing.T) {
	path := nativeFixture(t, "valid.rs", "fn visible() {}\n")
	invalid := nativeFixture(t, "invalid.rs", "\xff")
	out := nativeExecute(t, "inspect_file", invalid, path)
	want := fmt.Sprintf("--- %s ---\n1-1 function visible\n", path)
	if out.ExitCode != 1 || out.Stdout != want || !strings.Contains(out.Stderr, invalid) {
		t.Fatalf("Rust independent targets: %+v", out)
	}
	result := nativeInspectFixture(t, nativeFixture(t, "uppercase.RS", "\xff"))
	if result.Data.Kind != "none" || result.Data.LineCount != nil || len(result.Data.Outline) != 0 {
		t.Fatalf("uppercase extension was decoded: %+v", result.Data)
	}
	out = nativeExecute(t, "msymbol", "--workspace", filepath.Dir(path), "def", path, "visible")
	if out.ExitCode != 1 || out.Stdout != "" || !strings.Contains(out.Stderr, "unsupported msymbol source format") {
		t.Fatalf("Rust gained semantic support: %+v", out)
	}
}

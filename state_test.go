package mekugi

import (
	"strings"
	"testing"
)

func TestMekugi2FinalStateReport(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "file.txt", "alpha\nbeta\ngamma\n", 0o644)
	script := "in file.txt\ntype " + row(2, "beta") + ` "B"`
	result, err := applyForHostAtTest(t, root, script, "")
	if err != nil {
		t.Fatalf("ApplyForHost() error = %v, report %q", err, result.Report)
	}
	for _, fragment := range []string{
		"in file.txt\n",
		"last type 1 ranges 2:1-3:1\n",
		"files add=0 update=1 move=0 delete=0\n",
		"refs 2 type\n",
		"1:" + hashLine("alpha") + " alpha\n",
		"2:" + hashLine("B") + " B\n",
		"3:" + hashLine("gamma") + " gamma\n",
	} {
		if !strings.Contains(result.Report, fragment) {
			t.Fatalf("report %q lacks %q", result.Report, fragment)
		}
	}
}

func TestMekugi2FinalStateReportProvidesReusableReplacementTarget(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "file.txt", "alpha\nbeta\ngamma\n", 0o644)
	before := row(2, "beta")
	after := row(2, "B")
	firstScript := "in file.txt\ntype " + before + ` "B"`
	translated, err := TranslateForHostAt(t.Context(), root, firstScript, "")
	if err != nil {
		t.Fatal(err)
	}
	wantAlias := TargetAlias{Path: "file.txt", Before: before, After: after}
	if len(translated.TargetAliases) != 1 || translated.TargetAliases[0] != wantAlias {
		t.Fatalf("target aliases = %+v, want %+v", translated.TargetAliases, wantAlias)
	}
	result, err := applyForHostAtTest(t, root, firstScript, "")
	if err != nil {
		t.Fatalf("ApplyForHost() error = %v, report %q", err, result.Report)
	}

	rewritten := "in file.txt\ntype " + after + ` "C"`
	if result, err := applyForHostAtTest(t, root, rewritten, ""); err != nil {
		t.Fatalf("rewritten ApplyForHost() error = %v, report %q", err, result.Report)
	}
	if content := readTestFile(t, root, "file.txt"); content != "alpha\nC\ngamma\n" {
		t.Fatalf("rewritten content = %q", content)
	}
}

func TestMekugi2FormattedReferencesTrackEditedContent(t *testing.T) {
	root := t.TempDir()
	before := "package p\n\nvar ( a=1; b=2; c=3; d=4 )\n\nvar filler1=1\nvar filler2=2\nvar target=1\n"
	writeTestFile(t, root, "file.go", before, 0o644)
	script := "in file.go\ntype " + row(7, "var target=1") + ` "var target=2"`
	translated, err := TranslateForHostAt(t.Context(), root, script, "")
	if err != nil {
		t.Fatal(err)
	}
	result, err := applyForHostAtTest(t, root, script, "")
	if err != nil {
		t.Fatalf("ApplyForHost() error = %v, report %q", err, result.Report)
	}
	if !strings.Contains(result.Report, "refs 2 type\n") {
		t.Fatalf("formatted references %q lack command header", result.Report)
	}
	want := "12:" + hashLine("var target = 2") + " var target = 2\n"
	if !strings.Contains(result.Report, want) {
		t.Fatalf("formatted references %q lack edited row %q", result.Report, want)
	}
	wantAlias := TargetAlias{Path: "file.go", Before: row(7, "var target=1"), After: row(12, "var target = 2")}
	if len(translated.TargetAliases) != 1 || translated.TargetAliases[0] != wantAlias {
		t.Fatalf("formatted aliases = %+v, want %+v", translated.TargetAliases, wantAlias)
	}
}

func TestMekugi2FinalStateReportIsBounded(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "file.txt", "x x x x\n", 0o644)
	script := "in file.txt\ntype " + row(1, "x x x x") + ` "x" 4 "y"`
	result, err := applyForHostAtTest(t, root, script, "")
	if err != nil {
		t.Fatalf("ApplyForHost() error = %v, report %q", err, result.Report)
	}
	if !strings.Contains(result.Report, "last type 4 ranges ") || !strings.Contains(result.Report, " +1 more\n") {
		t.Fatalf("report = %q", result.Report)
	}
}

func TestMekugi2InsertionReportNamesTargetRange(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "file.txt", "alpha\nbeta\n", 0o644)
	script := "in file.txt\nadd " + row(2, "beta") + ` "inserted\n"`
	result, err := applyForHostAtTest(t, root, script, "")
	if err != nil {
		t.Fatalf("ApplyForHost() error = %v, report %q", err, result.Report)
	}
	if !strings.Contains(result.Report, "last add 1 ranges 2:1-3:1\n") {
		t.Fatalf("report = %q", result.Report)
	}
	if !strings.Contains(result.Report, "refs 2 add\n") {
		t.Fatalf("report = %q", result.Report)
	}
}

func TestMekugi2PreviewDoesNotInventTrailingEmptyLine(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "file.txt", "alpha\n", 0o644)
	result, err := applyForHostAtTest(t, root, "in file.txt", "")
	if err != nil {
		t.Fatalf("ApplyForHost() error = %v, report %q", err, result.Report)
	}
	want := "in file.txt\nlast none\nfiles add=0 update=0 move=0 delete=0\n1:" + hashLine("alpha") + " alpha\n"
	if result.Report != want {
		t.Fatalf("report = %q, want %q", result.Report, want)
	}
}

func TestMekugi2EmptyNewFileReport(t *testing.T) {
	root := t.TempDir()
	result, err := applyForHostAtTest(t, root, "new empty.txt", "")
	if err != nil {
		t.Fatalf("ApplyForHost() error = %v, report %q", err, result.Report)
	}
	want := "in empty.txt\nlast none\nfiles add=1 update=0 move=0 delete=0\n1:" + hashLine("") + " \n"
	if result.Report != want {
		t.Fatalf("report = %q, want %q", result.Report, want)
	}
}

func TestMekugi2MovedMutationReportUsesFinalPath(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "old.txt", "alpha\n", 0o644)
	script := "in old.txt\ntype " + row(1, "alpha") + ` "beta"` + "\nmv new.txt"
	result, err := applyForHostAtTest(t, root, script, "")
	if err != nil {
		t.Fatalf("ApplyForHost() error = %v, report %q", err, result.Report)
	}
	for _, want := range []string{
		"in new.txt\n",
		"last type 1 ranges 1:1-2:1\n",
		"files add=0 update=1 move=1 delete=0\n",
		"refs 2 type\n",
		"1:" + hashLine("beta") + " beta\n",
	} {
		if !strings.Contains(result.Report, want) {
			t.Fatalf("report %q lacks %q", result.Report, want)
		}
	}
}

func TestMekugi2PreviewBoundsContent(t *testing.T) {
	root := t.TempDir()
	content := strings.Repeat("界", 70) + "tail\n"
	writeTestFile(t, root, "file.txt", content, 0o644)
	result, err := applyForHostAtTest(t, root, "in file.txt", "")
	if err != nil {
		t.Fatalf("ApplyForHost() error = %v, report %q", err, result.Report)
	}
	rowText := strings.Split(strings.TrimSuffix(result.Report, "\n"), "\n")[3]
	if strings.Count(rowText, "界") != 64 || strings.Contains(rowText, "tail") {
		t.Fatalf("bounded preview row = %q", rowText)
	}
}

func TestMekugi2PreviewEscapesControls(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "file.txt", "a\x01b\n", 0o644)
	result, err := applyForHostAtTest(t, root, "in file.txt", "")
	if err != nil {
		t.Fatalf("ApplyForHost() error = %v, report %q", err, result.Report)
	}
	if strings.ContainsRune(result.Report, '\x01') || !strings.Contains(result.Report, `a\x01b`) {
		t.Fatalf("escaped report = %q", result.Report)
	}
}

func TestMekugi2FinalStateNoActiveFile(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "file.txt", "alpha\n", 0o644)
	result, err := applyForHostAtTest(t, root, "in file.txt\nrm", "")
	if err != nil {
		t.Fatalf("ApplyForHost() error = %v, report %q", err, result.Report)
	}
	want := "no active file\nlast none\nfiles add=0 update=0 move=0 delete=1\n"
	if result.Report != want {
		t.Fatalf("report = %q, want %q", result.Report, want)
	}
}

func TestMekugi2FinalReferencesCoverCommandsFilesAndContinuation(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "first.txt", "a0\na1\na2\na3\na4\n", 0o644)
	writeTestFile(t, root, "second.txt", "b0\nb1\nb2\n", 0o644)
	script := "in first.txt\n" +
		"type " + row(2, "a1") + " \"A1\"\n" +
		"type " + row(4, "a3") + " \"A3\"\n" +
		"in second.txt\n" +
		"add " + row(3, "b2") + " \"inserted\\n\""
	result, err := applyForHostAtTest(t, root, script, "")
	if err != nil {
		t.Fatalf("ApplyForHost() error = %v, report %q", err, result.Report)
	}

	firstHeader := "file first.txt\nrefs 2 type\n"
	secondHeader := "refs 3 type\n"
	thirdHeader := "file second.txt\nrefs 5 add\n"
	firstIndex := strings.Index(result.Report, firstHeader)
	secondIndex := strings.Index(result.Report, secondHeader)
	thirdIndex := strings.Index(result.Report, thirdHeader)
	if firstIndex < 0 || secondIndex <= firstIndex || thirdIndex <= secondIndex {
		t.Fatalf("reference block order in report %q", result.Report)
	}
	for _, current := range []string{
		"2:" + hashLine("A1") + " A1\n",
		"4:" + hashLine("A3") + " A3\n",
		"3:" + hashLine("inserted") + " inserted\n",
		"4:" + hashLine("b2") + " b2\n",
	} {
		if !strings.Contains(result.Report, current) {
			t.Fatalf("report %q lacks current row %q", result.Report, current)
		}
	}

	reportedTarget := row(4, "b2")
	if !strings.Contains(result.Report, reportedTarget+" b2\n") {
		t.Fatalf("report %q lacks reusable target %q", result.Report, reportedTarget)
	}
	continuation, continuationErr := applyForHostAtTest(t, root, "in second.txt\ntype "+reportedTarget+" \"B2\"", "")
	if continuationErr != nil {
		t.Fatalf("reported-row continuation error = %v, report %q", continuationErr, continuation.Report)
	}

	stale, staleErr := applyForHostAtTest(t, root, "in first.txt\ntype "+row(2, "a1")+" \"stale\"", "")
	if staleErr == nil || !strings.Contains(stale.Diagnostic, "row 2 is stale") {
		t.Fatalf("saved pre-edit row error = %v, diagnostic %q", staleErr, stale.Diagnostic)
	}
}

func TestMekugi2FinalReferencesProjectCollapsedDeletion(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "file.txt", "alpha\nbeta\ngamma\n", 0o644)
	result, err := applyForHostAtTest(t, root, "in file.txt\ntype "+row(2, "beta")+` ""`, "")
	if err != nil {
		t.Fatalf("ApplyForHost() error = %v, report %q", err, result.Report)
	}
	want := "refs 2 type\n" +
		"1:" + hashLine("alpha") + " alpha\n" +
		"2:" + hashLine("gamma") + " gamma\n"
	if !strings.Contains(result.Report, want) {
		t.Fatalf("deletion references %q lack %q", result.Report, want)
	}
}

func TestMekugi2FinalReferencesAreBoundedAndDeduplicated(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "file.txt", "zero\nx\none\ntwo\nx\nend\n", 0o644)
	script := "in file.txt\ntype " + row(2, "x") + ` "x" 2 "y"`
	result, err := applyForHostAtTest(t, root, script, "")
	if err != nil {
		t.Fatalf("ApplyForHost() error = %v, report %q", err, result.Report)
	}
	want := "refs 2 type\n" +
		"1:" + hashLine("zero") + " zero\n" +
		"2:" + hashLine("y") + " y\n" +
		"5:" + hashLine("y") + " y\n" +
		"6:" + hashLine("end") + " end\n"
	if !strings.Contains(result.Report, want) {
		t.Fatalf("bounded references %q lack %q", result.Report, want)
	}
}

func TestMekugi2FinalReferencesPreserveUneditedActiveEmptyFile(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "edited.txt", "old\n", 0o644)
	script := "in edited.txt\ntype " + row(1, "old") + " \"new\"\nnew empty.txt"
	result, err := applyForHostAtTest(t, root, script, "")
	if err != nil {
		t.Fatalf("ApplyForHost() error = %v, report %q", err, result.Report)
	}
	for _, want := range []string{
		"in empty.txt\n",
		"file edited.txt\nrefs 2 type\n",
		"1:" + hashLine("new") + " new\n",
		"file empty.txt\n1:" + hashLine("") + " \n",
	} {
		if !strings.Contains(result.Report, want) {
			t.Fatalf("report %q lacks %q", result.Report, want)
		}
	}
}

func TestPreviewTextMakesLeadingWhitespaceVisible(t *testing.T) {
	if got, want := previewText("  \tindented value"), `\x20\x20\tindented value`; got != want {
		t.Fatalf("previewText() = %q, want %q", got, want)
	}
}

func TestCompactReportDoesNotRepeatSingleFilePath(t *testing.T) {
	root := t.TempDir()
	path := root + "/probe.go"
	source := "package p\n\nvar a=1\n\nfunc f(){\nprintln(1);println(2)\n}\n"
	writeTestFile(t, root, "probe.go", source, 0o644)
	result, err := TranslateForHostAt(t.Context(), root, "in "+path+"\ntype \"package p\" \"package q\"", "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(result.Report, path) != 1 {
		t.Fatalf("single-file path repeated: %s", result.Report)
	}
	for _, want := range []string{"in " + path + "\n", "last type 1 ranges ", "refs 2 type\n", "format (pre-format -> final)\n", "Formatted "} {
		if !strings.Contains(result.Report, want) {
			t.Fatalf("report lacks %q: %s", want, result.Report)
		}
	}
	if strings.Contains(result.Report, "advisory ") {
		t.Fatalf("ordinary edit emitted advisory: %s", result.Report)
	}
}

func TestCompactReportScopesMultipleFormatterSections(t *testing.T) {
	for _, noActive := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty active", true: "no active"}[noActive], func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, root, "a.go", "package a\nvar x=1\n", 0o644)
			writeTestFile(t, root, "b.go", "package b\nvar y=2\n", 0o644)
			script := "in a.go\ntype \"package a\" \"package aa\"\nmv moved.go\n" +
				"in b.go\ntype \"package b\" \"package bb\"\nnew empty.txt\n"
			if noActive {
				script += "rm\n"
			}
			result, err := applyForHostAtTest(t, root, script, "")
			if err != nil {
				t.Fatal(err)
			}
			initial := "in empty.txt\n"
			if noActive {
				initial = "no active file\n"
			} else if !strings.Contains(result.Report, "file empty.txt\n"+row(1, "")+" \n") {
				t.Fatalf("fallback path not restored: %s", result.Report)
			}
			if !strings.HasPrefix(result.Report, initial) {
				t.Fatalf("active state lost: %s", result.Report)
			}
			for path, content := range map[string]string{"moved.go": "var x = 1", "b.go": "var y = 2"} {
				want := "file " + path + "\nformat (pre-format -> final)\nFormatted 2-3\n" +
					row(2, "") + " \n" + row(3, content) + " " + content + "\n"
				if !strings.Contains(result.Report, want) {
					t.Fatalf("formatter context lacks %q: %s", want, result.Report)
				}
			}
		})
	}
}

package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"html"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	markdownHTML "github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
	htmlParser "golang.org/x/net/html"
)

type markdownDocument struct {
	anchors map[string]bool
	links   []string
}

func checkDocs(root string) error {
	paths, err := documentationPaths(root)
	if err != nil {
		return err
	}

	cache := make(map[string]markdownDocument)
	read := func(path string) (markdownDocument, error) {
		path = filepath.Clean(path)
		if document, ok := cache[path]; ok {
			return document, nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return markdownDocument{}, err
		}
		document := parseMarkdown(data)
		cache[path] = document
		return document, nil
	}
	var failures []error
	for _, path := range paths {
		document, err := read(path)
		if err != nil {
			return err
		}
		for _, destination := range document.links {
			link, err := url.Parse(destination)
			if err != nil {
				failures = append(failures, fmt.Errorf("%s: invalid link %q: %w", path, destination, err))
				continue
			}
			if link.Scheme != "" || link.Host != "" {
				continue
			}
			target := path
			if link.Path != "" {
				if strings.HasPrefix(link.Path, "/") {
					target = filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(link.Path, "/")))
				} else {
					target = filepath.Join(filepath.Dir(path), filepath.FromSlash(link.Path))
				}
			}
			if _, err := os.Stat(target); err != nil {
				failures = append(failures, fmt.Errorf("%s: link %q: %w", path, destination, err))
				continue
			}
			if link.Fragment == "" || (filepath.Ext(target) != ".md" && filepath.Ext(target) != ".mdx") {
				continue
			}
			targetDocument, err := read(target)
			if err != nil {
				failures = append(failures, fmt.Errorf("%s: link %q: %w", path, destination, err))
				continue
			}
			if !targetDocument.anchors[link.Fragment] {
				failures = append(failures, fmt.Errorf("%s: missing heading anchor %q", path, destination))
			}
		}
	}
	if err := errors.Join(failures...); err != nil {
		return err
	}
	fmt.Printf("Checked local links and heading anchors in %d documents.\n", len(paths))
	return nil
}

func documentationPaths(root string) ([]string, error) {
	var paths []string
	if _, err := os.Stat(filepath.Join(root, ".git")); err == nil {
		// Include tracked files even when ignored, and exclude ignored local scratch.
		data, err := exec.Command("git", "-C", root, "ls-files", "--cached", "--others", "--exclude-standard", "-z").Output()
		if err != nil {
			return nil, fmt.Errorf("list documentation: %w", err)
		}
		for name := range strings.SplitSeq(string(data), "\x00") {
			if documentPath(name) {
				paths = append(paths, filepath.Join(root, name))
			}
		}
		return paths, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	// The commit hook supplies a checkout of the index without repository metadata.
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if excludedDocDirectory(entry.Name()) {
				return filepath.SkipDir
			}
		} else if documentPath(path) {
			paths = append(paths, path)
		}
		return nil
	})
	return paths, err
}

func documentPath(path string) bool {
	for part := range strings.SplitSeq(filepath.ToSlash(path), "/") {
		if excludedDocDirectory(part) {
			return false
		}
	}
	switch filepath.Ext(path) {
	case ".md", ".mdx", ".rst", ".adoc", ".txt":
		return true
	}
	return false
}

func excludedDocDirectory(name string) bool {
	switch name {
	case ".git", ".worktrees", "node_modules", "vendor", "testdata", "dist", "build":
		return true
	}
	return false
}

func parseMarkdown(source []byte) markdownDocument {
	document := markdownDocument{anchors: make(map[string]bool)}
	// Custom anchors do not participate in automatic heading numbering.
	headingIDs := make(map[string]bool)
	root := goldmark.DefaultParser().Parse(text.NewReader(source))
	var title bytes.Buffer
	writer := bufio.NewWriter(&title)
	_ = ast.Walk(root, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch node := node.(type) {
		case *ast.Link:
			document.links = append(document.links, string(util.URLEscape(node.Destination, true)))
		case *ast.Image:
			document.links = append(document.links, string(util.URLEscape(node.Destination, true)))
		case *ast.RawHTML:
			recordHTMLAnchors(node.Segments.Value(source), document.anchors)
		case *ast.HTMLBlock:
			recordHTMLAnchors(node.Lines().Value(source), document.anchors)
			if node.HasClosure() {
				recordHTMLAnchors(node.ClosureLine.Value(source), document.anchors)
			}
		case *ast.Heading:
			title.Reset()
			_ = ast.Walk(node, func(child ast.Node, entering bool) (ast.WalkStatus, error) {
				if entering {
					switch child := child.(type) {
					case *ast.Text:
						if child.Parent().Kind() == ast.KindCodeSpan {
							markdownHTML.DefaultWriter.RawWrite(writer, child.Value(source))
						} else {
							markdownHTML.DefaultWriter.Write(writer, child.Value(source))
						}
						if child.SoftLineBreak() || child.HardLineBreak() {
							_ = writer.WriteByte(' ')
						}
					case *ast.String:
						if child.IsCode() || child.IsRaw() {
							markdownHTML.DefaultWriter.RawWrite(writer, child.Value)
						} else {
							markdownHTML.DefaultWriter.Write(writer, child.Value)
						}
					}
				}
				return ast.WalkContinue, nil
			})
			_ = writer.Flush()
			base := strings.Map(func(r rune) rune {
				switch {
				case r == ' ':
					return '-'
				case unicode.IsLetter(r), unicode.IsNumber(r), unicode.IsMark(r), r == '-', r == '_':
					return unicode.ToLower(r)
				default:
					return -1
				}
			}, html.UnescapeString(title.String()))
			anchor := base
			for suffix := 1; headingIDs[anchor]; suffix++ {
				anchor = fmt.Sprintf("%s-%d", base, suffix)
			}
			headingIDs[anchor] = true
			document.anchors[anchor] = true
		}
		return ast.WalkContinue, nil
	})
	return document
}

func recordHTMLAnchors(source []byte, anchors map[string]bool) {
	tokens := htmlParser.NewTokenizer(bytes.NewReader(source))
	for {
		switch tokens.Next() {
		case htmlParser.ErrorToken:
			return
		case htmlParser.StartTagToken, htmlParser.SelfClosingTagToken:
			token := tokens.Token()
			for _, attribute := range token.Attr {
				if attribute.Key == "id" || (token.Data == "a" && attribute.Key == "name") {
					anchors[attribute.Val] = true
				}
			}
		}
	}
}

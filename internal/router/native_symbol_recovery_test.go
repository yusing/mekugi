package router

import (
	"bufio"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const msymbolRecoverySourceEnvironment = "MEKUGI_TEST_MSYMBOL_RECOVERY_SOURCE"

func installMSymbolRecoveryLSP(t *testing.T, source string) string {
	t.Helper()
	directory := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
	script := "#!/bin/sh\nunset " + routerTestWorkerEnvironment + "\n" +
		"export " + msymbolRecoverySourceEnvironment + "=" + quote(source) + "\n" +
		"exec " + quote(executable) + " -test.run='^TestMSymbolRecoveryLSPProcess$' -- \"$@\"\n"
	if err := os.WriteFile(filepath.Join(directory, "gopls"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return directory
}

// Invoked only by the short-lived fixture wrapper; no installed resolver needed.
func TestMSymbolRecoveryLSPProcess(t *testing.T) {
	source := os.Getenv(msymbolRecoverySourceEnvironment)
	if source == "" {
		return
	}
	uri := (&url.URL{Scheme: "file", Path: filepath.ToSlash(source)}).String()
	locations := make([]map[string]any, 0, 80)
	for index := range 80 {
		locations = append(locations, map[string]any{
			"uri": uri,
			"range": map[string]any{
				"start": map[string]int{"line": index + 2, "character": 14},
				"end":   map[string]int{"line": index + 2, "character": 20},
			},
		})
	}
	reader := bufio.NewReader(os.Stdin)
	for {
		length := -1
		for {
			header, err := reader.ReadString('\n')
			if err != nil {
				os.Exit(0)
			}
			if header == "\r\n" || header == "\n" {
				break
			}
			if value, ok := strings.CutPrefix(strings.TrimSpace(header), "Content-Length:"); ok {
				length, _ = strconv.Atoi(strings.TrimSpace(value))
			}
		}
		if length < 0 || length > 1<<20 {
			os.Exit(2)
		}
		body := make([]byte, length)
		if _, err := io.ReadFull(reader, body); err != nil {
			os.Exit(2)
		}
		var request struct {
			ID     jsontext.Value `json:"id"`
			Method string         `json:"method"`
		}
		if err := json.Unmarshal(body, &request); err != nil {
			os.Exit(2)
		}
		if request.Method == "exit" {
			os.Exit(0)
		}
		if len(request.ID) == 0 {
			continue
		}
		var result any
		switch request.Method {
		case "initialize":
			result = map[string]any{"capabilities": map[string]string{"positionEncoding": "utf-16"}}
		case "textDocument/references":
			result = locations
		}
		response := map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result}
		encoded, err := json.Marshal(&response)
		if err != nil {
			os.Exit(2)
		}
		if _, err := fmt.Fprintf(os.Stdout, "Content-Length: %d\r\n\r\n%s", len(encoded), encoded); err != nil {
			os.Exit(2)
		}
	}
}

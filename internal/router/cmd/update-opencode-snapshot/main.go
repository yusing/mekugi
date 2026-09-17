package main

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

const metadataURL = "https://models.opencode.ai/api.json"

var modelURLs = map[string]string{
	"opencode-go-models.json":  "https://opencode.ai/zen/go/v1/models",
	"opencode-zen-models.json": "https://opencode.ai/zen/v1/models",
}

func main() {
	output := flag.String("output", "opencode_snapshot", "snapshot output directory")
	flag.Parse()
	if err := run(*output); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(output string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client := &http.Client{Timeout: 30 * time.Second}
	metadata, err := get(ctx, client, metadataURL)
	if err != nil {
		return err
	}
	var providers map[string]jsontext.Value
	if err := json.Unmarshal(metadata, &providers); err != nil {
		return fmt.Errorf("decode OpenCode metadata: %w", err)
	}
	selected := make(map[string]jsontext.Value, 2)
	for _, id := range []string{"opencode", "opencode-go"} {
		provider, ok := providers[id]
		if !ok {
			return fmt.Errorf("OpenCode metadata is missing %q", id)
		}
		selected[id] = provider
	}
	metadata, err = json.Marshal(&selected, json.Deterministic(true), jsontext.WithIndent("  "))
	if err != nil {
		return fmt.Errorf("encode OpenCode metadata snapshot: %w", err)
	}
	files := map[string][]byte{"metadata.json": append(metadata, '\n')}
	for name, source := range modelURLs {
		body, err := get(ctx, client, source)
		if err != nil {
			return err
		}
		var list struct {
			Object string                      `json:"object"`
			Data   []map[string]jsontext.Value `json:"data"`
		}
		if err := json.Unmarshal(body, &list); err != nil {
			return fmt.Errorf("decode %s: %w", source, err)
		}
		if len(list.Data) == 0 {
			return fmt.Errorf("decode %s: empty model list", source)
		}
		for _, model := range list.Data {
			var id string
			if err := json.Unmarshal(model["id"], &id); err != nil || id == "" {
				return fmt.Errorf("decode %s: invalid model ID", source)
			}
			// The endpoint fills "created" with the response time on every call.
			// It is not model metadata, so omit it from the reproducible snapshot.
			delete(model, "created")
		}
		body, err = json.Marshal(&list, json.Deterministic(true), jsontext.WithIndent("  "))
		if err != nil {
			return fmt.Errorf("encode %s snapshot: %w", source, err)
		}
		files[name] = append(body, '\n')
	}
	if err := os.MkdirAll(output, 0o755); err != nil {
		return err
	}
	for name, body := range files {
		path := filepath.Join(output, name)
		temp, err := os.CreateTemp(output, "."+name+"-*")
		if err != nil {
			return err
		}
		tempPath := temp.Name()
		if _, err = temp.Write(body); err == nil {
			err = temp.Chmod(0o644)
		}
		if err == nil {
			err = temp.Close()
		} else {
			err = errors.Join(err, temp.Close())
		}
		if err == nil {
			err = os.Rename(tempPath, path)
		}
		os.Remove(tempPath)
		if err != nil {
			return err
		}
	}
	return nil
}

func get(ctx context.Context, client *http.Client, source string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", source, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch %s: %s", source, response.Status)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 16<<20+1))
	if err != nil {
		return nil, err
	}
	if len(body) > 16<<20 {
		return nil, fmt.Errorf("fetch %s: response exceeds 16 MiB", source)
	}
	return body, nil
}

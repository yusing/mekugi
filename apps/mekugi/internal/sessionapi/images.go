package sessionapi

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json/jsontext"
	"io"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/yusing/mekugi/libraries/persistence"
)

func imageIdentity(source string) string {
	sum := sha256.Sum256([]byte(source))
	return hex.EncodeToString(sum[:])
}

// readImage borrows the selected immutable publication, then reads outside its
// lock. The request can select a recorded image, never a filesystem operand.
func (s *Server) readImage(ctx context.Context, target requestTarget, body jsontext.Value) (any, *RPCError) {
	var request struct {
		Seq   uint64 `json:"seq"`
		Agent string `json:"agent"`
		Index int    `json:"index"`
		Image string `json:"image"`
	}
	if failure := decodeBody(body, &request); failure != nil {
		return nil, failure
	}
	source := ""
	s.mu.Lock()
	if s.session.Thread == target.thread && request.Index >= 0 {
		for _, feed := range []feed{s.main, s.agents} {
			if feed.publication == nil {
				continue
			}
			if _, visible := feed.entries[request.Seq]; !visible {
				continue
			}
			for _, entry := range feed.publication.Entries {
				if entry.Seq == request.Seq && entry.Agent == request.Agent && request.Index < len(entry.Images) {
					candidate := entry.Images[request.Index]
					if imageIdentity(candidate) == request.Image {
						source = candidate
					}
				}
			}
		}
	}
	s.mu.Unlock()
	if source == "" {
		return nil, rpcFailure(404, "This attached image is no longer available.")
	}
	if ctx.Err() != nil {
		return nil, rpcFailure(499, "Image request cancelled.")
	}
	var data []byte
	var err error
	if strings.HasPrefix(source, "data:") {
		header, encoded, ok := strings.Cut(source, ",")
		if !ok || !strings.HasSuffix(header, ";base64") || len(encoded) > (UploadLimit+2)/3*4 {
			return nil, rpcFailure(415, "Unsupported attached image data.")
		}
		data, err = base64.StdEncoding.DecodeString(encoded)
	} else {
		if strings.Contains(source, "://") {
			return nil, rpcFailure(415, "Remote image URLs are not loaded automatically.")
		}
		if !filepath.IsAbs(source) {
			if !filepath.IsAbs(target.workspace) {
				return nil, rpcFailure(400, "The image has no workspace.")
			}
			source = filepath.Join(target.workspace, source)
		}
		file, failure := persistence.OpenRetained(source, UploadLimit)
		if failure != nil {
			return nil, rpcFailure(404, "The attached image file is unavailable.")
		}
		defer file.Close()
		data, err = io.ReadAll(io.LimitReader(file, UploadLimit+1))
	}
	if err != nil || len(data) > UploadLimit {
		return nil, rpcFailure(413, "The attached image could not be read within the upload limit.")
	}
	if ctx.Err() != nil {
		return nil, rpcFailure(499, "Image request cancelled.")
	}
	mime := http.DetectContentType(data)
	switch mime {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
	default:
		return nil, rpcFailure(415, "The attachment is not a supported raster image.")
	}
	return struct {
		Mime string `json:"mime"`
		Data string `json:"data"`
	}{mime, base64.StdEncoding.EncodeToString(data)}, nil
}

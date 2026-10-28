package sessionapi

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"errors"
	"net/http"
	"strings"
	"time"
)

// ResourcePath carries admitted resource reads independently of control RPC.
const ResourcePath = "/api/resources/"

func (e *rpcEndpoint) resource(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	kind := strings.TrimPrefix(r.URL.Path, ResourcePath)
	limit := int64(1024)
	switch kind {
	case "attachment-image":
		limit = 64 << 10
	case "image", "content":
	default:
		http.NotFound(w, r)
		return
	}
	fail := func(failure *RPCError) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(failure.Data.Status)
		_ = json.MarshalWrite(w, failure)
	}
	// Cancellation retires a blocked HTTP body reader or writer as well as source preparation.
	stop := context.AfterFunc(r.Context(), func() {
		controller := http.NewResponseController(w)
		_ = controller.SetReadDeadline(time.Now())
		_ = controller.SetWriteDeadline(time.Now())
	})
	defer stop()
	var params RPCParams
	err := json.UnmarshalRead(http.MaxBytesReader(w, r.Body, limit+(64<<10)), &params, json.RejectUnknownMembers(true))
	if _, oversized := errors.AsType[*http.MaxBytesError](err); oversized || int64(len(params.Body)) > limit {
		fail(rpcFailure(413, "Resource request body too large."))
		return
	}
	if err != nil || params.Subscription != "" || params.Startup != "" {
		fail(rpcFailure(400, "Invalid resource request."))
		return
	}
	select {
	case e.preparations <- struct{}{}:
	default:
		fail(rpcFailure(503, "Resource transfer capacity is busy."))
		return
	}
	defer func() { <-e.preparations }()
	var value any
	var failure *RPCError
	if kind == "attachment-image" {
		value, failure = e.attachmentImage(r.Context(), params)
	} else {
		selected, denied := e.hub.lookup(params.Thread, "", remoteHumanView)
		if denied != nil {
			fail(denied)
			return
		}
		if kind == "content" {
			value, failure = selected.server.content(params.Body)
		} else {
			value, failure = selected.server.readImage(r.Context(), requestTarget{selected.entry.Thread, selected.entry.Workspace}, params.Body)
		}
	}
	if r.Context().Err() != nil {
		return
	}
	if failure != nil {
		fail(failure)
		return
	}
	if image, ok := value.(*imageResource); ok {
		w.Header().Set("Content-Type", image.mime)
		if _, err := bytes.NewReader(image.data).WriteTo(w); err != nil {
			panic(http.ErrAbortHandler)
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.MarshalWrite(w, value, nativeEncoding); err != nil {
		panic(http.ErrAbortHandler)
	}
}

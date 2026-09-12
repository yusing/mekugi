package capturer

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tiktoken-go/tokenizer"
)

func TestRecorderObservesSingleListenerAndProviderRetries(t *testing.T) {
	providerCalls := 0
	provider := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		providerCalls++
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Error(err)
			return
		}
		if !bytes.Contains(body, []byte(`"name":"hpatch"`)) {
			t.Errorf("provider request = %s", body)
		}
		writer.Header().Set("Content-Type", "application/json")
		if providerCalls == 1 {
			writer.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(writer, `{"error":{"message":"capacity"}}`)
			return
		}
		_, _ = io.WriteString(writer, `{"status":"completed","output":[{"type":"custom_tool_call","call_id":"call-1","name":"hpatch","input":"private edit"}],"usage":{"input_tokens":20,"input_tokens_details":{"cached_tokens":8},"output_tokens":5,"output_tokens_details":{"reasoning_tokens":2}}}`)
	}))
	defer provider.Close()

	capturePath := filepath.Join(t.TempDir(), "capture.jsonl")
	recorder, err := New(Config{Output: capturePath, Mode: "mekugi", ModelProtocol: "native"})
	if err != nil {
		t.Fatal(err)
	}
	defer recorder.Close()
	providerClient := &http.Client{Transport: recorder.Transport(http.DefaultTransport)}
	router := httptest.NewServer(recorder.Handler(http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
		if _, err := io.Copy(io.Discard, incoming.Body); err != nil {
			t.Error(err)
			return
		}
		providerBody := []byte(`{"model":"model","tools":[{"type":"custom","name":"hpatch"}]}`)
		for attempt := range 2 {
			request, err := http.NewRequestWithContext(incoming.Context(), http.MethodPost, provider.URL+"/responses", bytes.NewReader(providerBody))
			if err != nil {
				t.Error(err)
				return
			}
			response, err := providerClient.Do(request)
			if err != nil {
				t.Error(err)
				return
			}
			_, _ = io.Copy(io.Discard, response.Body)
			if attempt == 1 {
				ObserveProviderUsage(incoming.Context(), ProviderUsage{
					InputTokens: 20, CachedTokens: 8, OutputTokens: 5, ReasoningTokens: 2,
				})
			}
			_ = response.Body.Close()
			if attempt == 0 {
				continue
			}
			writer.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(writer, `{"status":"completed","output":[{"type":"custom_tool_call","call_id":"call-1","name":"exec","input":"// mekugi-proxy: apply translated patch\nawait tools.apply_patch(\"private patch\");\ntext(\"done\");"}]}`)
		}
	})))
	defer router.Close()

	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, router.URL+"/v1/responses", strings.NewReader(`{"model":"model","input":"private prompt"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("thread-id", "thread-1")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	visible, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(visible, []byte(`"name":"exec"`)) {
		t.Fatalf("visible response = %s", visible)
	}

	payload, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(payload, []byte("private")) {
		t.Fatalf("capture retained private content: %s", payload)
	}
	var records []captureRecord
	scanner := bufio.NewScanner(bytes.NewReader(payload))
	for scanner.Scan() {
		var record captureRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			t.Fatal(err)
		}
		records = append(records, record)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if len(records) != 3 {
		t.Fatalf("records = %#v", records)
	}
	first, second, front := records[0], records[1], records[2]
	if first.SchemaVersion != schemaVersion || first.CaptureID == "" || first.CaptureID != second.CaptureID || first.CaptureID != front.CaptureID ||
		first.ProviderAttempt != 1 || second.ProviderAttempt != 2 || front.ProviderAttempt != 0 ||
		first.RequestSequence != 1 || second.RequestSequence != 1 || front.RequestSequence != 1 ||
		first.ResponseStatus != "http_error" || !first.ResponseComplete || first.Usage != nil ||
		second.RequestModel != "model" || second.Usage == nil || second.Usage.InputTokens != 20 ||
		second.Request.Bytes == 0 || second.Response.Tokens == 0 || front.Request.Tokens == 0 || front.Response.Bytes == 0 ||
		len(second.ToolCalls) != 1 || second.ToolCalls[0].Name != "hpatch" ||
		len(front.ToolCalls) != 1 || front.ToolCalls[0].Name != "exec" || second.ToolCalls[0].CallID != front.ToolCalls[0].CallID {
		t.Fatalf("captured exchanges = %#v", records)
	}
	snapshot := recorder.snapshot()
	if snapshot.Requests.Logical != 1 || snapshot.Requests.ProviderAttempts != 2 || snapshot.Requests.Completed != 1 ||
		snapshot.Usage.ProviderAttempts != 1 || snapshot.Usage.InputTokens != 20 || snapshot.Usage.CachedInputTokens != 8 ||
		snapshot.Transport.ClientRequests.Bytes == 0 || snapshot.Transport.ProviderAttemptRequests.Bytes != first.Request.Bytes+second.Request.Bytes ||
		snapshot.ProviderTools["hpatch"].Calls != 1 || snapshot.DeliveredTools["exec"].Calls != 1 ||
		snapshot.Mekugi.Calls != 1 || snapshot.Mekugi.Successful != 1 || snapshot.Mekugi.Rejected != 0 ||
		snapshot.Mekugi.ProviderInputTokens != second.ToolCalls[0].InputTokens ||
		snapshot.Mekugi.DeliveredInputTokens != front.ToolCalls[0].InputTokens ||
		snapshot.Protocol.InputPayloadTokensSaved != 0 ||
		snapshot.Semantic.ClientOutputs.Tokens != front.FinalOutput.Tokens ||
		snapshot.Semantic.ProviderAttemptOutputs.Tokens != second.FinalOutput.Tokens ||
		snapshot.Protocol.OutputPayloadTokensExpansion != signedDifference(front.FinalOutput.Tokens, second.FinalOutput.Tokens) ||
		snapshot.Capture.Records != 3 || snapshot.Capture.CaptureErrors != 0 || snapshot.Capture.Incomplete != 0 ||
		snapshot.Capture.MissingProvider != 0 || snapshot.Capture.AttemptGaps != 0 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

type terminalResponseBody struct {
	content []byte
	closed  bool
}

func (body *terminalResponseBody) Read(destination []byte) (int, error) {
	if len(body.content) == 0 {
		return 0, io.EOF
	}
	count := copy(destination, body.content)
	body.content = body.content[count:]
	return count, nil
}

func (body *terminalResponseBody) Close() error {
	body.closed = true
	return nil
}

func TestRecorderAcceptsConsumerCloseAfterTerminalResponse(t *testing.T) {
	recorder, err := New(Config{Mode: "mekugi", ModelProtocol: "native"})
	if err != nil {
		t.Fatal(err)
	}
	state := &requestState{captureID: "capture", sequence: 1}
	providerBody := &terminalResponseBody{content: []byte(`{"status":"completed","usage":{"input_tokens":3}}`)}
	transport := recorder.Transport(roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       providerBody,
		}, nil
	}))
	request := httptest.NewRequest(http.MethodPost, "https://provider.example/responses", strings.NewReader(`{"model":"model"}`))
	request = request.WithContext(context.WithValue(request.Context(), captureKey{}, state))
	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, len(providerBody.content))
	if _, err := io.ReadFull(response.Body, payload); err != nil {
		t.Fatal(err)
	}
	if _, err := response.Body.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("terminal read error = %v, want EOF", err)
	}
	if records := state.providerRecords(); len(records) != 0 {
		t.Fatalf("provider record finalized before shared usage: %#v", records)
	}
	ObserveProviderUsage(request.Context(), ProviderUsage{InputTokens: 3})
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}

	records := state.providerRecords()
	if len(records) != 1 || records[0].CaptureError != "" || !records[0].ResponseComplete || records[0].Usage == nil {
		t.Fatalf("provider records = %#v", records)
	}
	if !providerBody.closed {
		t.Fatal("provider response body was not closed")
	}
}

type trackedRequestBody struct {
	reader io.Reader
	closed bool
}

func (body *trackedRequestBody) Read(destination []byte) (int, error) {
	return body.reader.Read(destination)
}

func (body *trackedRequestBody) Close() error {
	body.closed = true
	return nil
}

func TestRecorderClosesAndRestoresNonReplayableProviderRequest(t *testing.T) {
	recorder, err := New(Config{Mode: "mekugi", ModelProtocol: "native"})
	if err != nil {
		t.Fatal(err)
	}
	original := &trackedRequestBody{reader: strings.NewReader(`{"model":"model"}`)}
	var forwarded []byte
	transport := recorder.Transport(roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		forwarded, err = io.ReadAll(request.Body)
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"status":"completed"}`))}, err
	}))
	request := httptest.NewRequest(http.MethodPost, "https://provider.example/responses", nil)
	request.Body = original
	request.GetBody = nil
	request = request.WithContext(context.WithValue(request.Context(), captureKey{}, &requestState{captureID: "capture", sequence: 1}))
	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if !original.closed || string(forwarded) != `{"model":"model"}` {
		t.Fatalf("original closed = %t, forwarded = %q", original.closed, forwarded)
	}
}

func TestRecorderDoesNotForwardPartiallyReadProviderRequest(t *testing.T) {
	recorder, err := New(Config{Mode: "mekugi", ModelProtocol: "native"})
	if err != nil {
		t.Fatal(err)
	}
	readErr := errors.New("sentinel read failure")
	original := &trackedRequestBody{reader: io.MultiReader(strings.NewReader(`{"model":`), failingReader{err: readErr})}
	forwarded := false
	transport := recorder.Transport(roundTripperFunc(func(*http.Request) (*http.Response, error) {
		forwarded = true
		return nil, errors.New("unexpected forwarding")
	}))
	request := httptest.NewRequest(http.MethodPost, "https://provider.example/responses", nil)
	request.Body = original
	request.GetBody = nil
	request = request.WithContext(context.WithValue(request.Context(), captureKey{}, &requestState{captureID: "capture", sequence: 1}))
	if _, err := transport.RoundTrip(request); !errors.Is(err, readErr) {
		t.Fatalf("RoundTrip error = %v", err)
	}
	if forwarded || !original.closed {
		t.Fatalf("forwarded = %t, original closed = %t", forwarded, original.closed)
	}
}

func TestRecorderClosesOriginalProviderRequestWhenReplayFails(t *testing.T) {
	recorder, err := New(Config{Mode: "mekugi", ModelProtocol: "native"})
	if err != nil {
		t.Fatal(err)
	}
	replayErr := errors.New("sentinel replay failure")
	original := &trackedRequestBody{reader: strings.NewReader(`{"model":"model"}`)}
	forwarded := false
	transport := recorder.Transport(roundTripperFunc(func(*http.Request) (*http.Response, error) {
		forwarded = true
		return nil, errors.New("unexpected forwarding")
	}))
	request := httptest.NewRequest(http.MethodPost, "https://provider.example/responses", nil)
	request.Body = original
	request.GetBody = func() (io.ReadCloser, error) { return nil, replayErr }
	request = request.WithContext(context.WithValue(request.Context(), captureKey{}, &requestState{captureID: "capture", sequence: 1}))
	if _, err := transport.RoundTrip(request); !errors.Is(err, replayErr) {
		t.Fatalf("RoundTrip error = %v", err)
	}
	if forwarded || !original.closed {
		t.Fatalf("forwarded = %t, original closed = %t", forwarded, original.closed)
	}
}

type failingReader struct {
	err error
}

func (reader failingReader) Read([]byte) (int, error) {
	return 0, reader.err
}

func TestSnapshotAccountsCacheCorrectionsDiagnosticsAndMissingEvidence(t *testing.T) {
	recorder, err := New(Config{Mode: "mekugi", ModelProtocol: "native"})
	if err != nil {
		t.Fatal(err)
	}
	complete := func(record captureRecord) captureRecord {
		record.SchemaVersion = schemaVersion
		record.Mode = "mekugi"
		record.ModelProtocol = "native"
		record.StatusCode = http.StatusOK
		record.ResponseStatus = "completed"
		record.ResponseComplete = true
		return record
	}
	usage := func(input, cached, output uint64) *ProviderUsage {
		return &ProviderUsage{InputTokens: input, CachedTokens: cached, OutputTokens: output}
	}
	states := make(map[string]*requestState)
	for _, record := range []captureRecord{
		complete(captureRecord{Boundary: "provider", CaptureID: "first", RequestSequence: 1, ProviderAttempt: 1, ThreadID: "thread", Usage: usage(100, 20, 10)}),
		complete(captureRecord{Boundary: "codex", CaptureID: "first", RequestSequence: 1, ThreadID: "thread"}),
		complete(captureRecord{Boundary: "provider", CaptureID: "second", RequestSequence: 2, ProviderAttempt: 1, ThreadID: "thread", Usage: usage(120, 80, 12), ToolCalls: []toolCallMetrics{{CallID: "correction", Name: "hpatch_recover", InputTokens: 4}}}),
		complete(captureRecord{Boundary: "codex", CaptureID: "second", RequestSequence: 2, ThreadID: "thread", ToolCalls: []toolCallMetrics{{CallID: "correction", Name: "exec", InputTokens: 10, Kind: "mekugi_diagnostic", Diagnostic: "row-stale"}}}),
		complete(captureRecord{Boundary: "codex", CaptureID: "missing", RequestSequence: 3, ThreadID: "thread"}),
	} {
		state := states[record.CaptureID]
		if state == nil {
			state = &requestState{captureID: record.CaptureID, sequence: record.RequestSequence, threadID: record.ThreadID}
			states[record.CaptureID] = state
			if state.threadID != "" {
				recorder.cacheQueues[state.threadID] = append(recorder.cacheQueues[state.threadID], state)
			}
		}
		if record.Boundary == "provider" {
			state.addProvider(record)
		}
		recorder.write(record, state)
	}

	snapshot := recorder.snapshot()
	if snapshot.Usage.InputTokens != 220 || snapshot.Usage.CachedInputTokens != 100 ||
		snapshot.Cache.ProviderCacheRate == nil || *snapshot.Cache.ProviderCacheRate != float64(100)/220 ||
		snapshot.Cache.ColdOrNewUncachedInputTokens != 100 || snapshot.Cache.EligiblePrefixTokens != 100 ||
		snapshot.Cache.EligiblePrefixCachedTokens != 80 || snapshot.Cache.EligiblePrefixMissTokens != 20 ||
		snapshot.Cache.EligiblePrefixCacheRate == nil || *snapshot.Cache.EligiblePrefixCacheRate != 0.8 ||
		snapshot.Mekugi.Calls != 1 || snapshot.Mekugi.Corrections != 1 || snapshot.Mekugi.Rejected != 1 ||
		snapshot.Mekugi.Diagnostics["row-stale"] != 1 || snapshot.Mekugi.CarrierInputTokensExpansion != 6 ||
		snapshot.Capture.MissingProvider != 1 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func TestSnapshotUsesTerminalOutputOnceInsteadOfWholeSSEStream(t *testing.T) {
	recorder, err := New(Config{Mode: "mekugi", ModelProtocol: "native"})
	if err != nil {
		t.Fatal(err)
	}
	state := &requestState{
		captureID: "stream",
		sequence:  1,
		providerUsage: map[uint64]ProviderUsage{1: {
			InputTokens: 10, CachedTokens: 8, OutputTokens: 4,
		}},
	}
	providerItem := `{"type":"custom_tool_call","call_id":"call","name":"hpatch","input":"edit"}`
	clientItem := `{"type":"custom_tool_call","call_id":"call","name":"exec","input":"text(\"done\");"}`
	providerOutput := `[` + providerItem + `]`
	clientOutput := `[` + clientItem + `]`
	providerDone := `{"type":"response.output_item.done","output_index":0,"item":` + providerItem + `}`
	clientDone := `{"type":"response.output_item.done","output_index":0,"item":` + clientItem + `}`
	providerTerminal := `{"type":"response.completed","response":{"status":"completed","tools":[{"type":"custom","name":"hpatch","description":` + strconv.Quote(strings.Repeat("large provider tool definition ", 400)) + `}],"output":[],"usage":{"input_tokens":10,"input_tokens_details":{"cached_tokens":8},"output_tokens":4}}}`
	clientTerminal := `{"type":"response.completed","response":{"status":"completed","tools":[{"type":"custom","name":"apply_patch"}],"output":[]}}`
	providerStream := strings.Repeat("data: {\"type\":\"response.custom_tool_call_input.delta\",\"delta\":\""+strings.Repeat("x", 400)+"\"}\n\n", 200) +
		"data: " + providerDone + "\n\ndata: " + providerTerminal + "\n\n"
	clientStream := strings.Repeat("data: {\"type\":\"response.in_progress\"}\n\n", 200) +
		"data: " + clientDone + "\n\ndata: " + clientTerminal + "\n\n"
	requestBody := []byte(`{"model":"model"}`)
	recorder.recordExchange(
		state, "provider", 1, time.Now(), requestBody,
		observedPayload{content: []byte(providerStream), bytes: uint64(len(providerStream))},
		http.StatusOK, "text/event-stream", "", nil, providerResponseEvidence{},
	)
	recorder.recordExchange(
		state, "codex", 0, time.Now(), requestBody,
		observedPayload{content: []byte(clientStream), bytes: uint64(len(clientStream))},
		http.StatusOK, "text/event-stream", "", nil, providerResponseEvidence{},
	)

	snapshot := recorder.snapshot()
	exchange := snapshot.Exchanges[0]
	provider := exchange.ProviderAttempts[0]
	providerOutputMetrics, err := recorder.measure([]byte(providerOutput))
	if err != nil {
		t.Fatal(err)
	}
	clientOutputMetrics, err := recorder.measure([]byte(clientOutput))
	if err != nil {
		t.Fatal(err)
	}
	want := signedDifference(clientOutputMetrics.Tokens, providerOutputMetrics.Tokens)
	raw := signedDifference(exchange.ClientResponse.Tokens, provider.Response.Tokens)
	if exchange.ClientFinalOutput != clientOutputMetrics || provider.FinalOutput != providerOutputMetrics ||
		snapshot.Protocol.OutputPayloadTokensExpansion != want || snapshot.Protocol.OutputPayloadTokensExpansion == raw {
		t.Fatalf("semantic protocol = %d, want %d; raw stream difference = %d; snapshot %#v", snapshot.Protocol.OutputPayloadTokensExpansion, want, raw, snapshot)
	}
	if snapshot.Usage.OutputTokens != 4 || snapshot.Usage.ProviderAttempts != 1 {
		t.Fatalf("provider usage = %#v", snapshot.Usage)
	}
}

func TestObserveResponseMeasuresOnlyTerminalOutput(t *testing.T) {
	recorder, err := New(Config{Mode: "mekugi", ModelProtocol: "ctp2"})
	if err != nil {
		t.Fatal(err)
	}
	firstItem := `{"type":"message","content":[{"type":"output_text","text":"decoded CTP text"}]}`
	secondItem := `{"type":"custom_tool_call","call_id":"call","name":"hpatch","input":"edit"}`
	output := `[` + firstItem + `,` + secondItem + `]`
	response := `{"status":"completed","tools":[{"description":` + strconv.Quote(strings.Repeat("unrelated tool metadata ", 200)) + `}],"output":` + output + `}`
	tests := map[string]struct {
		payload     string
		contentType string
	}{
		"JSON": {payload: response, contentType: "application/json"},
		"SSE": {
			payload: "data: {\"type\":\"response.in_progress\"}\n\n" +
				"data: {\"type\":\"response.output_item.done\",\"output_index\":1,\"item\":" + secondItem + "}\n\n" +
				"data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":" + firstItem + "}\n\n" +
				"data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n",
			contentType: "text/event-stream",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			var record captureRecord
			if got := observeResponse([]byte(test.payload), test.contentType, &record, recorder.codec); string(got) != output {
				t.Fatalf("terminal output = %s, want %s", got, output)
			}
			if record.ResponseStatus != "completed" {
				t.Fatalf("response status = %q", record.ResponseStatus)
			}
		})
	}
}

func TestRouterHTTPRejectionDoesNotBecomeCaptureCorruption(t *testing.T) {
	recorder, err := New(Config{Mode: "mekugi", ModelProtocol: "native"})
	if err != nil {
		t.Fatal(err)
	}
	handler := recorder.Handler(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		http.Error(writer, "responses request cannot satisfy the required mekugi rewrite", http.StatusBadGateway)
	}))
	handler.ServeHTTP(
		httptest.NewRecorder(),
		httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"model"}`)),
	)
	snapshot := recorder.snapshot()
	if snapshot.Requests.Failed != 1 || snapshot.Capture.CaptureErrors != 0 || snapshot.Capture.MissingProvider != 0 {
		t.Fatalf("router rejection snapshot = %#v", snapshot)
	}
}

func TestSignedDifferenceSaturates(t *testing.T) {
	maximum := uint64(^uint64(0) >> 1)
	if got := signedDifference(^uint64(0), 0); got != int64(maximum) {
		t.Fatalf("positive saturation = %d", got)
	}
	if got := signedDifference(0, ^uint64(0)); got != -int64(maximum)-1 {
		t.Fatalf("negative saturation = %d", got)
	}
}

func TestSnapshotBoundsExchangeDetailWithoutLosingTotals(t *testing.T) {
	recorder, err := New(Config{Mode: "mekugi", ModelProtocol: "native"})
	if err != nil {
		t.Fatal(err)
	}
	for sequence := uint64(1); sequence <= maxRetainedExchangeDetails+1; sequence++ {
		state := &requestState{captureID: fmt.Sprintf("capture-%d", sequence), sequence: sequence}
		provider := captureRecord{
			Boundary: "provider", CaptureID: state.captureID, RequestSequence: sequence, ProviderAttempt: 1,
			StatusCode: http.StatusOK, ResponseStatus: "completed", ResponseComplete: true,
			Usage: &ProviderUsage{InputTokens: 1},
		}
		state.addProvider(provider)
		recorder.write(provider, state)
		recorder.write(captureRecord{
			Boundary: "codex", CaptureID: state.captureID, RequestSequence: sequence,
			StatusCode: http.StatusOK, ResponseStatus: "completed", ResponseComplete: true,
		}, state)
	}

	snapshot := recorder.snapshot()
	if snapshot.Requests.Logical != maxRetainedExchangeDetails+1 || snapshot.Usage.InputTokens != maxRetainedExchangeDetails+1 {
		t.Fatalf("cumulative metrics = requests %d, input %d", snapshot.Requests.Logical, snapshot.Usage.InputTokens)
	}
	if len(snapshot.Exchanges) != maxRetainedExchangeDetails || snapshot.Exchanges[0].Sequence != 2 {
		t.Fatalf("retained exchanges = %d starting at %d", len(snapshot.Exchanges), snapshot.Exchanges[0].Sequence)
	}
	if snapshot.Capture.DroppedExchangeDetails != 1 || snapshot.Capture.Records != 2*(maxRetainedExchangeDetails+1) {
		t.Fatalf("capture health = %#v", snapshot.Capture)
	}
}

func TestRecorderPreservesStreamingOnTheWrappedListener(t *testing.T) {
	recorder, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	server := httptest.NewServer(recorder.Handler(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, "data: first\n\n")
		_ = http.NewResponseController(writer).Flush()
		<-release
		_, _ = io.WriteString(writer, "data: second\n\n")
	})))
	defer server.Close()

	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/v1/responses", strings.NewReader(`{"model":"model"}`))
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(response.Body)
	line, err := reader.ReadString('\n')
	if err != nil || line != "data: first\n" {
		t.Fatalf("first line = %q, error %v", line, err)
	}
	close(release)
	remainder, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if string(remainder) != "\ndata: second\n\n" {
		t.Fatalf("remainder = %q", remainder)
	}
}

type discardStreamingWriter struct {
	header  http.Header
	written int
	flushed chan struct{}
	once    sync.Once
}

func (writer *discardStreamingWriter) Header() http.Header { return writer.header }
func (writer *discardStreamingWriter) WriteHeader(int)     {}
func (writer *discardStreamingWriter) Write(payload []byte) (int, error) {
	writer.written += len(payload)
	return len(payload), nil
}
func (writer *discardStreamingWriter) Flush() {
	writer.once.Do(func() { close(writer.flushed) })
}

func TestRecorderBoundsLargeStreamingObservationAfterFirstFlush(t *testing.T) {
	recorder, err := New(Config{Mode: "mekugi", ModelProtocol: "native"})
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	writer := &discardStreamingWriter{header: make(http.Header), flushed: make(chan struct{})}
	handler := recorder.Handler(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(response, "data: first\n\n")
		if err := http.NewResponseController(response).Flush(); err != nil {
			t.Error(err)
		}
		<-release
		chunk := bytes.Repeat([]byte{'x'}, 64<<10)
		for range maxObservedResponseBytes/(64<<10) + 1 {
			_, _ = response.Write(chunk)
		}
	}))
	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.ServeHTTP(writer, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"model"}`)))
	}()
	<-writer.flushed
	if writer.written != len("data: first\n\n") {
		t.Fatalf("bytes written before release = %d", writer.written)
	}
	close(release)
	<-done

	snapshot := recorder.snapshot()
	if snapshot.Capture.CaptureErrors != 1 || snapshot.Capture.Incomplete != 1 || len(snapshot.Exchanges) != 1 {
		t.Fatalf("capture health = %#v", snapshot.Capture)
	}
	if snapshot.Exchanges[0].ClientResponse.Bytes != uint64(writer.written) || writer.written <= maxObservedResponseBytes {
		t.Fatalf("observed response bytes = %d, writer bytes = %d", snapshot.Exchanges[0].ClientResponse.Bytes, writer.written)
	}
	var observation boundedObservation
	for range maxObservedResponseBytes/(64<<10) + 1 {
		_, _ = observation.Write(bytes.Repeat([]byte{'x'}, 64<<10))
	}
	if observation.content.Len() != maxObservedResponseBytes || !observation.overflow {
		t.Fatalf("bounded observation retained %d bytes, overflow %t", observation.content.Len(), observation.overflow)
	}
}

func TestRecorderFinalizesClientCaptureWhenHandlerPanics(t *testing.T) {
	recorder, err := New(Config{Mode: "mekugi", ModelProtocol: "native"})
	if err != nil {
		t.Fatal(err)
	}
	handler := recorder.Handler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("sentinel")
	}))
	func() {
		defer func() {
			if recovered := recover(); recovered != "sentinel" {
				t.Fatalf("recovered panic = %#v", recovered)
			}
		}()
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"model"}`)))
	}()
	snapshot := recorder.snapshot()
	if snapshot.Capture.Records != 1 || snapshot.Capture.Incomplete != 1 || snapshot.Requests.Logical != 1 {
		t.Fatalf("snapshot after panic = %#v", snapshot)
	}
}

func TestDecodedCapturePayloadReadsGzip(t *testing.T) {
	plain := []byte(`{"status":"completed"}`)
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(plain); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	decoded, err := decodedCapturePayload(compressed.Bytes(), "gzip")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded, plain) {
		t.Fatalf("decoded payload = %q", decoded)
	}
}

func TestDecodedCapturePayloadBoundsGzipExpansion(t *testing.T) {
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	chunk := bytes.Repeat([]byte{'x'}, 64<<10)
	for range maxObservedResponseBytes/(64<<10) + 1 {
		if _, err := writer.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := decodedCapturePayload(compressed.Bytes(), "gzip"); !errors.Is(err, errDecodedPayloadTooLarge) {
		t.Fatalf("decodedCapturePayload error = %v", err)
	}
}

func TestObserveResponseJoinsMultilineSSEData(t *testing.T) {
	payload := []byte("event: response.completed\n" +
		"data: {\"type\":\"response.completed\",\n" +
		"data: \"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":3}}}\n\n")
	codec, err := tokenizer.ForModel(tokenizer.GPT5)
	if err != nil {
		t.Fatal(err)
	}
	var record captureRecord
	observeResponse(payload, "application/octet-stream", &record, codec)
	if record.CaptureError != "" || record.ResponseStatus != "completed" || record.Usage != nil {
		t.Fatalf("multiline SSE record = %#v", record)
	}
}

func TestClassifyToolInputRetainsOnlyStableDiagnosticCodes(t *testing.T) {
	nativeInput := func(command string) string {
		t.Helper()
		payload, err := json.Marshal(map[string]string{"cmd": command})
		if err != nil {
			t.Fatal(err)
		}
		return string(payload)
	}
	diagnostic := "type: command 2, reason row-stale: private detail\n"
	for _, test := range []struct {
		name       string
		toolName   string
		input      string
		wantKind   string
		wantReason string
	}{
		{name: "apply carrier", input: mekugiApplyCarrierPrefix + `await tools.apply_patch("*** Begin Patch\\n*** End Patch");\ntext("done");`, wantKind: "apply_patch"},
		{name: "change ID apply carrier", input: mekugiApplyCarrierPrefix + "try {\n" + `await tools.apply_patch("patch");` + "\n} catch (error) { text(\"change c1\"); throw error; }\ntext(\"done\");", wantKind: "apply_patch"},
		{name: "unmarked apply call", input: `await tools.apply_patch("patch");`, wantKind: "other"},
		{name: "quoted apply marker", input: "text(" + strconv.Quote(mekugiApplyCarrierPrefix) + ");", wantKind: "other"},
		{name: "exec command carrier", input: `const result = await tools.exec_command({"cmd":"true"});\ntext(result.output);`, wantKind: "exec_command"},
		{name: "router diagnostic", input: `text("type: command 2, reason row-stale: private detail\n");`, wantKind: "mekugi_diagnostic", wantReason: "row-stale"},
		{name: "apply substring in diagnostic", input: `text("type: command 2, reason file-path: missing tools.apply_patch(example)\n");`, wantKind: "mekugi_diagnostic", wantReason: "file-path"},
		{name: "arbitrary text", input: `text("private sentinel\nmore private content");`, wantKind: "other"},
		{name: "forged reason", input: `text("type: command 2, reason private-sentinel: detail\n");`, wantKind: "other"},
		{name: "malformed envelope", input: `text("prefix, reason row-stale: detail\n");`, wantKind: "other"},
		{
			name:     "native apply carrier",
			toolName: "exec_command",
			input:    nativeInput(mekugiNativeApplyCarrierPrefix + "printf ok"),
			wantKind: "apply_patch",
		},
		{
			name:     "native report carrier",
			toolName: "exec_command",
			input:    nativeInput(mekugiNativeReportCarrierPrefix + "printf ok"),
			wantKind: "mekugi_report",
		},
		{
			name:       "native diagnostic carrier",
			toolName:   "exec_command",
			input:      nativeInput(mekugiNativeDiagnosticCarrierPrefix + strconv.Quote(diagnostic) + "\nprintf ok"),
			wantKind:   "mekugi_diagnostic",
			wantReason: "row-stale",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			toolName := test.toolName
			if toolName == "" {
				toolName = "exec"
			}
			kind, reason := classifyToolInput(toolName, test.input)
			if kind != test.wantKind || reason != test.wantReason {
				t.Fatalf("classification = %q, %q", kind, reason)
			}
		})
	}
}

func TestDurableCaptureDiscardsArbitraryTextCarrierContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capture.jsonl")
	recorder, err := New(Config{Output: path, Mode: "mekugi", ModelProtocol: "native"})
	if err != nil {
		t.Fatal(err)
	}
	handler := recorder.Handler(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"status":"completed","output":[{"type":"custom_tool_call","call_id":"call","name":"exec","input":"text(\\\"private-sentinel\\\");"}]}`)
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"model"}`)))
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(payload, []byte("private-sentinel")) {
		t.Fatalf("durable capture retained arbitrary carrier content: %s", payload)
	}
	snapshot := recorder.snapshot()
	if snapshot.Mekugi.Diagnostics != nil && len(snapshot.Mekugi.Diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v", snapshot.Mekugi.Diagnostics)
	}
}

func TestCacheAttributionUsesFinalAttemptOfPrecedingLogicalRequest(t *testing.T) {
	recorder, err := New(Config{Mode: "mekugi", ModelProtocol: "native"})
	if err != nil {
		t.Fatal(err)
	}
	writeExchange := func(sequence uint64, thread string, usages ...ProviderUsage) {
		state := &requestState{captureID: fmt.Sprintf("cache-%d", sequence), sequence: sequence, threadID: thread}
		if thread != "" {
			recorder.cacheQueues[thread] = append(recorder.cacheQueues[thread], state)
		}
		for index, value := range usages {
			provider := captureRecord{
				Boundary: "provider", CaptureID: state.captureID, RequestSequence: sequence,
				ProviderAttempt: uint64(index + 1), ThreadID: thread, StatusCode: http.StatusOK,
				ResponseStatus: "completed", ResponseComplete: true, Usage: &value,
			}
			state.addProvider(provider)
			recorder.write(provider, state)
		}
		recorder.write(captureRecord{
			Boundary: "codex", CaptureID: state.captureID, RequestSequence: sequence, ThreadID: thread,
			StatusCode: http.StatusOK, ResponseStatus: "completed", ResponseComplete: true,
		}, state)
	}
	writeExchange(1, "thread", ProviderUsage{InputTokens: 100}, ProviderUsage{InputTokens: 120})
	writeExchange(2, "thread", ProviderUsage{InputTokens: 150, CachedTokens: 100})
	writeExchange(3, "", ProviderUsage{InputTokens: 80})
	writeExchange(4, "", ProviderUsage{InputTokens: 90, CachedTokens: 80})
	writeExchange(5, "thread", ProviderUsage{InputTokens: 200, CachedTokens: 150})
	state := &requestState{captureID: "cache-6", sequence: 6, threadID: "thread"}
	recorder.cacheQueues["thread"] = append(recorder.cacheQueues["thread"], state)
	provider := captureRecord{
		Boundary: "provider", CaptureID: state.captureID, RequestSequence: state.sequence,
		ProviderAttempt: 1, ThreadID: state.threadID, StatusCode: http.StatusTooManyRequests,
		ResponseStatus: "http_error", ResponseComplete: true,
	}
	state.addProvider(provider)
	recorder.write(provider, state)
	recorder.write(captureRecord{
		Boundary: "codex", CaptureID: state.captureID, RequestSequence: state.sequence, ThreadID: state.threadID,
		StatusCode: http.StatusTooManyRequests, ResponseStatus: "http_error", ResponseComplete: true,
	}, state)
	writeExchange(7, "thread", ProviderUsage{InputTokens: 220, CachedTokens: 200})

	cache := recorder.snapshot().Cache
	if cache.EligiblePrefixTokens != 270 || cache.EligiblePrefixCachedTokens != 250 ||
		cache.EligiblePrefixMissTokens != 20 || cache.ColdOrNewUncachedInputTokens != 310 {
		t.Fatalf("cache metrics = %#v", cache)
	}
}

func TestCacheAttributionFollowsRequestSequenceWhenResponsesFinishOutOfOrder(t *testing.T) {
	recorder, err := New(Config{Mode: "mekugi", ModelProtocol: "native"})
	if err != nil {
		t.Fatal(err)
	}
	states := make([]*requestState, 3)
	header := make(http.Header)
	header.Set("thread-id", "thread")
	for index := range states {
		states[index], err = recorder.beginRequest(header)
		if err != nil {
			t.Fatal(err)
		}
	}
	finish := func(state *requestState, usage ProviderUsage) {
		provider := captureRecord{
			Boundary: "provider", CaptureID: state.captureID, RequestSequence: state.sequence,
			ProviderAttempt: 1, ThreadID: state.threadID, StatusCode: http.StatusOK,
			ResponseStatus: "completed", ResponseComplete: true, Usage: &usage,
		}
		state.addProvider(provider)
		recorder.write(provider, state)
		recorder.write(captureRecord{
			Boundary: "codex", CaptureID: state.captureID, RequestSequence: state.sequence, ThreadID: state.threadID,
			StatusCode: http.StatusOK, ResponseStatus: "completed", ResponseComplete: true,
		}, state)
	}
	finish(states[1], ProviderUsage{InputTokens: 120, CachedTokens: 80})
	finish(states[0], ProviderUsage{InputTokens: 100})
	finish(states[2], ProviderUsage{InputTokens: 130, CachedTokens: 110})

	cache := recorder.snapshot().Cache
	if cache.EligiblePrefixTokens != 220 || cache.EligiblePrefixCachedTokens != 190 ||
		cache.EligiblePrefixMissTokens != 30 || cache.ColdOrNewUncachedInputTokens != 130 {
		t.Fatalf("cache metrics = %#v", cache)
	}
}

func TestRecorderIgnoresUnregisteredResponsesSuffix(t *testing.T) {
	recorder, err := New(Config{Mode: "mekugi", ModelProtocol: "native"})
	if err != nil {
		t.Fatal(err)
	}
	handler := recorder.Handler(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNotFound)
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/invalid/responses", strings.NewReader(`{"model":"model"}`)))
	if snapshot := recorder.snapshot(); snapshot.Capture.Records != 0 || snapshot.Requests.Logical != 0 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

// Return bytes with the cancellation error to exercise the final in-flight update.
type closeReleasedBody struct {
	reading chan struct{}
	closed  chan struct{}
}

func (b *closeReleasedBody) Read(p []byte) (int, error) {
	close(b.reading)
	<-b.closed
	return copy(p, "last"), io.ErrClosedPipe
}
func (b *closeReleasedBody) Close() error { close(b.closed); return nil }

func TestObservedResponseCloseWaitsForReadObservation(t *testing.T) {
	upstream := &closeReleasedBody{reading: make(chan struct{}), closed: make(chan struct{})}
	var payload observedPayload
	var readErr error
	body := &observedResponseBody{ReadCloser: upstream, finish: func(p observedPayload, err error) { payload, readErr = p, err }}
	readDone := make(chan struct{})
	go func() { defer close(readDone); _, _ = body.Read(make([]byte, 10)) }()
	<-upstream.reading
	if err := body.Close(); err != nil {
		t.Fatal(err)
	}
	<-readDone
	if string(payload.content) != "last" || !errors.Is(readErr, io.ErrClosedPipe) {
		t.Fatalf("payload=%q error=%v", payload.content, readErr)
	}
}

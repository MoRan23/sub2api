package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// WrapOpenAIExcelResponse translates JSON and SSE without a second upstream
// request. Closing the returned body closes the physical upstream body too.
func WrapOpenAIExcelResponse(ctx context.Context, response *http.Response, state *OpenAIExcelWireState) (*http.Response, error) {
	if response == nil || response.Body == nil || state == nil {
		return nil, errors.New("invalid Excel response")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return response, nil
	}
	copyResponse := new(http.Response)
	*copyResponse = *response
	copyResponse.Header = response.Header.Clone()
	copyResponse.Header.Del("Content-Length")
	copyResponse.ContentLength = -1
	if strings.Contains(strings.ToLower(response.Header.Get("Content-Type")), "text/event-stream") {
		reader, writer := io.Pipe()
		body := &openAIExcelResponseBody{reader: reader, upstream: response.Body}
		stop := context.AfterFunc(ctx, func() { _ = body.Close() })
		go func() {
			defer stop()
			defer func() { _ = response.Body.Close() }()
			err := state.transformSSE(ctx, response.Body, writer)
			if err != nil && ctx.Err() == nil {
				// Preserve an explicit protocol failure, never fabricate a completed
				// response from item.done or a disconnected stream.
				payload, _ := json.Marshal(map[string]any{"type": "error", "code": "excel_protocol_error", "message": openAIExcelSafeProtocolError(err)})
				_, _ = fmt.Fprintf(writer, "event: error\ndata: %s\n\n", payload)
			}
			_ = writer.CloseWithError(err)
		}()
		copyResponse.Body = body
		return copyResponse, nil
	}
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(response.Body, openAIExcelMaxWireBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > openAIExcelMaxWireBytes {
		return nil, errors.New("excel response exceeds size limit")
	}
	var payload map[string]any
	if openAIExcelJSON(raw, &payload) != nil || payload == nil {
		return nil, errors.New("excel response is not a JSON object")
	}
	state.observeModel(payload)
	if openAIExcelString(payload["status"]) != "completed" {
		return nil, errors.New("excel response did not contain a successful completion")
	}
	if err := state.translateResponse(ctx, payload); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	copyResponse.Body = io.NopCloser(bytes.NewReader(encoded))
	copyResponse.ContentLength = int64(len(encoded))
	copyResponse.Header.Set("Content-Type", "application/json")
	return copyResponse, nil
}

type openAIExcelResponseBody struct {
	reader   *io.PipeReader
	upstream io.ReadCloser
	once     sync.Once
}

func (b *openAIExcelResponseBody) Read(p []byte) (int, error) { return b.reader.Read(p) }
func (b *openAIExcelResponseBody) Close() error {
	var err error
	b.once.Do(func() { _ = b.reader.Close(); err = b.upstream.Close() })
	return err
}

func openAIExcelSafeProtocolError(err error) string {
	if errors.Is(err, ErrOpenAIExcelHistoryNotFound) {
		return "Excel native tool history is unavailable"
	}
	// Parsing errors can contain upstream arguments. Keep those out of logs and
	// protocol errors, while the internal Go error remains available to tests.
	return "Excel upstream response could not be translated safely"
}

func (s *OpenAIExcelWireState) observeModel(payload map[string]any) {
	if s.options.ObserveModel == nil {
		return
	}
	if response := openAIExcelMap(payload["response"]); response != nil {
		if model := openAIExcelString(response["model"]); model != "" {
			s.options.ObserveModel(model)
		}
	}
	if model := openAIExcelString(payload["model"]); model != "" {
		s.options.ObserveModel(model)
	}
}

func (s *OpenAIExcelWireState) translateResponse(ctx context.Context, response map[string]any) error {
	output, ok := response["output"].([]any)
	if !ok {
		if s.required {
			return errors.New("excel response did not satisfy required tool_choice")
		}
		return nil
	}
	translated := make([]any, 0, len(output))
	calls := 0
	nativeCalls := 0
	for _, raw := range output {
		item := openAIExcelMap(raw)
		if item == nil {
			return errors.New("invalid Excel output item")
		}
		kind := openAIExcelString(item["type"])
		if kind == "function_call" || kind == "custom_tool_call" {
			nativeCalls++
			calls++
			if !s.parallel && calls > 1 {
				// Excel may still return a second native call when the client
				// explicitly disabled parallel calls.  Keep the first valid call;
				// the client will submit its result before asking for the next one.
				calls--
				continue
			}
			call, err := s.translateNativeCall(ctx, item)
			if err != nil {
				// A malformed or undeclared native call must never leak through to
				// Codex.  The reference bridge drops unusable calls and preserves
				// any valid text/reasoning or tool calls in the same response.  Only
				// storage and cancellation errors are fatal because they make a
				// valid client call impossible to replay safely.
				if errors.Is(err, ErrOpenAIExcelHistoryNotFound) ||
					errors.Is(err, ErrOpenAIExcelHistoryStorageUnavailable) ||
					ctx.Err() != nil {
					return err
				}
				calls--
				continue
			}
			call["status"] = "completed"
			translated = append(translated, call)
		} else {
			openAIExcelNormalizeReasoning(item)
			translated = append(translated, item)
		}
	}
	response["output"] = translated
	if s.required && calls == 0 {
		return errors.New("excel response did not satisfy required tool_choice")
	}
	// If a response consisted solely of native tool calls and all of them were
	// malformed, returning an empty successful response would hide an upstream
	// protocol problem.  Optional tools are safe to drop when there is other
	// assistant output, but an all-tool response needs a fresh model turn.
	if nativeCalls > 0 && len(translated) == 0 && len(output) == nativeCalls {
		return errors.New("excel response contained no usable client tool call")
	}
	return nil
}

func openAIExcelNormalizeReasoning(item map[string]any) {
	if openAIExcelString(item["type"]) != "reasoning" {
		return
	}
	text := openAIExcelPartsText(item["summary"])
	if text == "" {
		text = openAIExcelPartsText(item["content"])
	}
	if text != "" {
		item["summary"] = []any{map[string]any{"type": "summary_text", "text": text}}
	}
}

func openAIExcelPartsText(raw any) string {
	if text, ok := raw.(string); ok {
		return text
	}
	parts, ok := raw.([]any)
	if !ok {
		return ""
	}
	var out strings.Builder
	for _, part := range parts {
		if text, ok := part.(string); ok {
			_, _ = out.WriteString(text)
		} else {
			_, _ = out.WriteString(openAIExcelString(openAIExcelMap(part)["text"]))
		}
	}
	return out.String()
}

type openAIExcelSSEWriter struct {
	w        io.Writer
	sequence int64
}

func (w *openAIExcelSSEWriter) event(kind string, payload map[string]any) error {
	payload["type"] = kind
	payload["sequence_number"] = w.sequence
	w.sequence++
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w.w, "event: %s\ndata: %s\n\n", kind, raw)
	return err
}

func (w *openAIExcelSSEWriter) tool(call map[string]any, index int) error {
	item := make(map[string]any, len(call))
	for key, value := range call {
		item[key] = value
	}
	key, prefix := "arguments", "response.function_call_arguments"
	if openAIExcelString(call["type"]) == "custom_tool_call" {
		key, prefix = "input", "response.custom_tool_call_input"
	}
	item["status"] = "in_progress"
	item[key] = ""
	if err := w.event("response.output_item.added", map[string]any{"output_index": index, "item": item}); err != nil {
		return err
	}
	if err := w.event(prefix+".delta", map[string]any{"output_index": index, "item_id": call["id"], "delta": call[key]}); err != nil {
		return err
	}
	if err := w.event(prefix+".done", map[string]any{"output_index": index, "item_id": call["id"], key: call[key]}); err != nil {
		return err
	}
	return w.event("response.output_item.done", map[string]any{"output_index": index, "item": call})
}

func (s *OpenAIExcelWireState) transformSSE(ctx context.Context, body io.Reader, destination io.Writer) error {
	reader := bufio.NewReaderSize(body, 32<<10)
	writer := openAIExcelSSEWriter{w: destination}
	var event string
	var data bytes.Buffer
	items := make(map[int]map[string]any)
	bufferedBytes := 0
	var keepaliveResponse map[string]any
	const keepaliveInterval = 15 * time.Second
	type readResult struct {
		line string
		err  error
	}
	readResults := make(chan readResult, 1)
	go func() {
		for {
			line, err := openAIExcelReadSSELine(reader)
			select {
			case readResults <- readResult{line: line, err: err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	process := func() (bool, error) {
		if data.Len() == 0 {
			event = ""
			return false, nil
		}
		text := bytes.TrimSuffix(data.Bytes(), []byte{'\n'})
		if bytes.Equal(text, []byte("[DONE]")) {
			return false, errors.New("excel stream ended without a completion event")
		}
		var payload map[string]any
		if openAIExcelJSON(text, &payload) != nil || payload == nil {
			return false, errors.New("invalid Excel SSE payload")
		}
		s.observeModel(payload)
		kind := event
		if kind == "" {
			kind = openAIExcelString(payload["type"])
		}
		if kind == "" || strings.ContainsAny(kind, "\r\n") {
			return false, errors.New("invalid Excel SSE event type")
		}
		if kind == "response.created" || kind == "response.in_progress" {
			if response := openAIExcelMap(payload["response"]); response != nil {
				// Keepalive must repeat only an upstream response identity we
				// actually observed.  Do not invent a response or model.
				keepaliveResponse = response
			}
		}
		event = ""
		data.Reset()
		switch kind {
		case "response.failed", "response.incomplete", "error":
			// Stop at the first failure; a late completion cannot turn it into success.
			openAIExcelStripNativeOutput(payload)
			return true, writer.event(kind, payload)
		case "response.completed", "response.done":
			response := openAIExcelMap(payload["response"])
			if response == nil {
				return false, errors.New("excel completion has no response")
			}
			if status := openAIExcelString(response["status"]); status != "" && status != "completed" {
				return false, errors.New("excel completion has unsuccessful status")
			}
			if _, hasOutput := response["output"]; !hasOutput && len(items) > 0 {
				positions := make([]int, 0, len(items))
				for pos := range items {
					positions = append(positions, pos)
				}
				sort.Ints(positions)
				out := make([]any, 0, len(positions))
				for _, pos := range positions {
					out = append(out, items[pos])
				}
				response["output"] = out
			}
			if err := s.translateResponse(ctx, response); err != nil {
				return false, err
			}
			if output, ok := response["output"].([]any); ok {
				for index, raw := range output {
					item := openAIExcelMap(raw)
					kind := openAIExcelString(item["type"])
					if kind == "function_call" || kind == "custom_tool_call" {
						if err := writer.tool(item, index); err != nil {
							return false, err
						}
					}
				}
			}
			// Some Excel/WebView2 versions call the terminal event
			// response.done.  It carries the same completed response shape;
			// normalize it so Codex sees the canonical Responses event.
			return true, writer.event("response.completed", payload)
		case "response.function_call_arguments.delta", "response.function_call_arguments.done", "response.custom_tool_call_input.delta", "response.custom_tool_call_input.done":
			// Do not expose the Excel executor or its argument schema to a client.
			return false, nil
		case "response.output_item.added", "response.output_item.done":
			item := openAIExcelMap(payload["item"])
			if item == nil {
				return false, errors.New("excel item event has no item")
			}
			kindItem := openAIExcelString(item["type"])
			if kind == "response.output_item.done" {
				index := len(items)
				if number, ok := payload["output_index"].(json.Number); ok {
					if i, err := number.Int64(); err == nil && i >= 0 && i < 1<<20 {
						index = int(i)
					}
				}
				raw, _ := json.Marshal(item)
				bufferedBytes += len(raw)
				if bufferedBytes > openAIExcelMaxWireBytes || len(items) > 4096 {
					return false, errors.New("excel response items exceed size limit")
				}
				items[index] = item
			}
			if kindItem == "function_call" || kindItem == "custom_tool_call" {
				return false, nil
			}
			openAIExcelNormalizeReasoning(item)
		}
		openAIExcelStripNativeOutput(payload)
		return false, writer.event(kind, payload)
	}
	ticker := time.NewTicker(keepaliveInterval)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var line string
		var err error
		select {
		case result := <-readResults:
			line, err = result.line, result.err
		case <-ticker.C:
			if keepaliveResponse != nil {
				if err := writer.event("response.in_progress", map[string]any{"response": keepaliveResponse}); err != nil {
					return err
				}
			}
			continue
		}
		if len(line) > openAIExcelMaxItemBytes || data.Len()+len(line) > openAIExcelMaxItemBytes {
			return errors.New("excel SSE event exceeds size limit")
		}
		if len(line) > 0 {
			line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
			if line == "" {
				terminal, processErr := process()
				if processErr != nil {
					return processErr
				}
				if terminal {
					return nil
				}
			} else if strings.HasPrefix(line, "data:") {
				_, _ = data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
				_ = data.WriteByte('\n')
			} else if strings.HasPrefix(line, "event:") {
				event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			} else if strings.HasPrefix(line, ":") {
				if _, writeErr := fmt.Fprintln(destination, line); writeErr != nil {
					return writeErr
				}
			}
		}
		if err != nil {
			if err != io.EOF {
				return err
			}
			if data.Len() > 0 {
				terminal, processErr := process()
				if processErr != nil {
					return processErr
				}
				if terminal {
					return nil
				}
			}
			return errors.New("excel stream closed before its terminal event")
		}
	}
}

// Opening/progress/failure responses may contain partially assembled output.
// Native tool identities must only leave through the validated completion path.
func openAIExcelStripNativeOutput(payload map[string]any) {
	for _, response := range []map[string]any{payload, openAIExcelMap(payload["response"])} {
		output, ok := response["output"].([]any)
		if !ok {
			continue
		}
		kept := make([]any, 0, len(output))
		for _, raw := range output {
			item := openAIExcelMap(raw)
			kind := openAIExcelString(item["type"])
			if kind == "function_call" || kind == "custom_tool_call" {
				continue
			}
			kept = append(kept, raw)
		}
		response["output"] = kept
	}
}

func openAIExcelReadSSELine(reader *bufio.Reader) (string, error) {
	var line strings.Builder
	for {
		part, err := reader.ReadSlice('\n')
		if line.Len()+len(part) > openAIExcelMaxItemBytes {
			return "", errors.New("excel SSE line exceeds size limit")
		}
		_, _ = line.Write(part)
		if err != bufio.ErrBufferFull {
			return line.String(), err
		}
	}
}

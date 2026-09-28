package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/tidwall/gjson"
)

var ErrOpenAIBackendHistoryUnavailable = errors.New("upstream session history cannot be restored; start a new session")

func openAIBackendProvenanceRequired(account *Account) bool {
	return account != nil && SupportsOpenAIExcelUpstream(account) && (account.IsOpenAIExcelUpstreamEnabled() || account.OpenAIUpstreamRouteGeneration() != "")
}

func backendOpaqueValues(body []byte) []string {
	var root any
	if json.Unmarshal(body, &root) != nil {
		return nil
	}
	values := make([]string, 0)
	var visit func(any, int)
	visit = func(node any, depth int) {
		if depth > 32 {
			return
		}
		switch v := node.(type) {
		case map[string]any:
			if encrypted, ok := v["encrypted_content"].(string); ok && encrypted != "" {
				values = append(values, "opaque:"+excelStateDigest(encrypted))
			}
			for key, child := range v {
				if key == "input" || key == "output" || key == "response" || key == "item" {
					visit(child, depth+1)
				}
			}
		case []any:
			for _, child := range v {
				visit(child, depth+1)
			}
		}
	}
	visit(root, 0)
	return values
}

func (s *OpenAIGatewayService) validateOpenAIBackendPayload(ctx context.Context, account *Account, scope string, body []byte, headers http.Header) error {
	if !openAIBackendProvenanceRequired(account) {
		return nil
	}
	ids := backendOpaqueValues(body)
	if previous := gjson.GetBytes(body, "previous_response_id").String(); previous != "" {
		ids = append(ids, "response:"+previous)
	}
	if state := headers.Get("x-codex-turn-state"); state != "" {
		ids = append(ids, "state:"+excelStateDigest(state))
	}
	for _, id := range ids {
		if _, err := s.excelState.get(ctx, scope, "provenance", id); err != nil {
			return ErrOpenAIBackendHistoryUnavailable
		}
	}
	return nil
}

func (s *OpenAIGatewayService) rememberOpenAIBackendPayload(ctx context.Context, account *Account, scope string, body []byte, headers http.Header) error {
	if !openAIBackendProvenanceRequired(account) {
		return nil
	}
	ids := backendOpaqueValues(body)
	typ := gjson.GetBytes(body, "type").String()
	if typ == "response.completed" || gjson.GetBytes(body, "status").String() == "completed" {
		if id := firstNonEmpty(gjson.GetBytes(body, "response.id").String(), gjson.GetBytes(body, "id").String()); id != "" {
			ids = append(ids, "response:"+id)
		}
	}
	if state := headers.Get("x-codex-turn-state"); state != "" {
		ids = append(ids, "state:"+excelStateDigest(state))
	}
	for _, id := range ids {
		if err := s.excelState.put(ctx, scope, "provenance", id, json.RawMessage(`{"observed":true}`), openAIExcelNativeTTL, 4096); err != nil {
			return err
		}
	}
	return nil
}

func requestReplayBytes(req *http.Request) ([]byte, error) {
	if req == nil || req.GetBody == nil {
		return nil, nil
	}
	body, err := req.GetBody()
	if err != nil {
		return nil, err
	}
	defer func() { _ = body.Close() }()
	data, err := io.ReadAll(io.LimitReader(body, (64<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 64<<20 {
		return nil, errors.New("upstream request exceeds replay limit")
	}
	return data, nil
}

func (s *OpenAIGatewayService) validateOpenAIBackendRequest(req *http.Request, account *Account, scope string) error {
	if req == nil || !openAIBackendProvenanceRequired(account) || !strings.HasSuffix(req.URL.Path, "/responses") {
		return nil
	}
	body, err := requestReplayBytes(req)
	if err != nil {
		return ErrOpenAIBackendHistoryUnavailable
	}
	return s.validateOpenAIBackendPayload(req.Context(), account, scope, body, req.Header)
}

type excelObservedBody struct {
	pipe   *io.PipeReader
	source io.ReadCloser
	once   sync.Once
}

func (b *excelObservedBody) Read(p []byte) (int, error) { return b.pipe.Read(p) }
func (b *excelObservedBody) Close() error {
	var err error
	b.once.Do(func() { _ = b.pipe.Close(); err = b.source.Close() })
	return err
}

func (s *OpenAIGatewayService) wrapOpenAIBackendResponse(req *http.Request, resp *http.Response, account *Account, scope string) *http.Response {
	if req == nil || resp == nil || resp.Body == nil || resp.StatusCode >= 400 || !openAIBackendProvenanceRequired(account) || !strings.HasSuffix(req.URL.Path, "/responses") {
		return resp
	}
	source := resp.Body
	reader, writer := io.Pipe()
	observed := &excelObservedBody{pipe: reader, source: source}
	resp.Body = observed
	stop := context.AfterFunc(req.Context(), func() { _ = observed.Close() })
	go func() {
		defer stop()
		defer func() { _ = source.Close() }()
		var failure error
		defer func() { _ = writer.CloseWithError(failure) }()
		if err := s.rememberOpenAIBackendPayload(req.Context(), account, scope, nil, resp.Header); err != nil {
			failure = err
			return
		}
		if strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
			scanner := bufio.NewScanner(source)
			scanner.Buffer(make([]byte, 16<<10), 16<<20)
			var frame, data bytes.Buffer
			flush := func() error {
				payload := bytes.TrimSpace(data.Bytes())
				if json.Valid(payload) {
					if err := s.rememberOpenAIBackendPayload(req.Context(), account, scope, payload, nil); err != nil {
						return err
					}
				}
				_, err := writer.Write(frame.Bytes())
				frame.Reset()
				data.Reset()
				return err
			}
			for scanner.Scan() {
				line := scanner.Bytes()
				if frame.Len()+len(line)+1 > 16<<20 {
					failure = errors.New("upstream event exceeds session observation limit")
					return
				}
				_, _ = frame.Write(line)
				_ = frame.WriteByte('\n')
				if bytes.HasPrefix(line, []byte("data:")) {
					_, _ = data.Write(bytes.TrimPrefix(bytes.TrimPrefix(line, []byte("data:")), []byte(" ")))
					_ = data.WriteByte('\n')
				}
				if len(line) == 0 {
					if err := flush(); err != nil {
						failure = err
						return
					}
				}
			}
			failure = scanner.Err()
			if failure == nil && frame.Len() > 0 {
				failure = flush()
			}
			return
		}
		body, err := io.ReadAll(io.LimitReader(source, (64<<20)+1))
		if err != nil {
			failure = err
			return
		}
		if len(body) > 64<<20 {
			failure = errors.New("upstream response exceeds session observation limit")
			return
		}
		if err := s.rememberOpenAIBackendPayload(req.Context(), account, scope, body, nil); err != nil {
			failure = err
			return
		}
		_, failure = writer.Write(body)
	}()
	return resp
}

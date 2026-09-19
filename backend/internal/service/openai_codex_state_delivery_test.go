package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type codexStateDeliveryFailWriter struct{ header http.Header }

func (w *codexStateDeliveryFailWriter) Header() http.Header { return w.header }
func (*codexStateDeliveryFailWriter) WriteHeader(int)       {}
func (*codexStateDeliveryFailWriter) Write([]byte) (int, error) {
	return 0, errors.New("client disconnected")
}
func (*codexStateDeliveryFailWriter) Flush() {}

func TestCodexStateHTTPDeliveryRequiresSuccessfulSemanticOutput(t *testing.T) {
	const model = "gpt-5.5"
	const responseJSON = `{"id":"resp_delivery","status":"completed","model":"gpt-5.5","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":2,"output_tokens":1}}`
	const created = "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_delivery\",\"model\":\"gpt-5.5\",\"output\":[]}}\n\n"
	const metadata = "data: {\"type\":\"response.metadata\",\"headers\":{\"x-codex-turn-state\":\"candidate\"}}\n\n"
	const failedJSON = `{"id":"resp_delivery","status":"failed","model":"gpt-5.5","output":[],"error":{"type":"invalid_request_error","message":"invalid input"},"usage":{"input_tokens":2,"output_tokens":0}}`
	successSSE := created + "data: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_1\",\"output_index\":0,\"content_index\":0,\"delta\":\"hello\"}\n\n" + metadata + "data: {\"type\":\"response.completed\",\"response\":" + responseJSON + "}\n\n"
	failedSSE := created + metadata + "data: {\"type\":\"response.failed\",\"response\":" + failedJSON + "}\n\n"
	type handler func(*OpenAIGatewayService, *http.Response, *gin.Context, *Account) error
	handlers := []struct {
		name      string
		stream    bool
		plainJSON bool
		handle    handler
	}{
		{"responses_stream", true, false, func(s *OpenAIGatewayService, r *http.Response, c *gin.Context, a *Account) error {
			_, err := s.handleStreamingResponse(c.Request.Context(), r, c, a, time.Now(), model, model)
			return err
		}},
		{"passthrough_stream", true, false, func(s *OpenAIGatewayService, r *http.Response, c *gin.Context, a *Account) error {
			_, err := s.handleStreamingResponsePassthrough(c.Request.Context(), r, c, a, time.Now(), model, model)
			return err
		}},
		{"chat_stream", true, false, func(s *OpenAIGatewayService, r *http.Response, c *gin.Context, a *Account) error {
			_, err := s.handleChatStreamingResponse(r, c, a, model, model, model, time.Now(), 1)
			return err
		}},
		{"messages_stream", true, false, func(s *OpenAIGatewayService, r *http.Response, c *gin.Context, a *Account) error {
			_, err := s.handleAnthropicStreamingResponse(r, c, a, model, model, model, time.Now())
			return err
		}},
		{"responses_json", false, true, func(s *OpenAIGatewayService, r *http.Response, c *gin.Context, a *Account) error {
			_, err := s.handleNonStreamingResponse(c.Request.Context(), r, c, a, model, model)
			return err
		}},
		{"passthrough_json", false, true, func(s *OpenAIGatewayService, r *http.Response, c *gin.Context, a *Account) error {
			_, err := s.handleNonStreamingResponsePassthrough(c.Request.Context(), r, c, a, model, model)
			return err
		}},
		{"responses_sse_to_json", false, false, func(s *OpenAIGatewayService, r *http.Response, c *gin.Context, a *Account) error {
			_, err := s.handleNonStreamingResponse(c.Request.Context(), r, c, a, model, model)
			return err
		}},
		{"passthrough_sse_to_json", false, false, func(s *OpenAIGatewayService, r *http.Response, c *gin.Context, a *Account) error {
			_, err := s.handleNonStreamingResponsePassthrough(c.Request.Context(), r, c, a, model, model)
			return err
		}},
		{"chat_json", false, false, func(s *OpenAIGatewayService, r *http.Response, c *gin.Context, a *Account) error {
			_, err := s.handleChatBufferedStreamingResponse(r, c, a, model, model, model, time.Now())
			return err
		}},
		{"messages_json", false, false, func(s *OpenAIGatewayService, r *http.Response, c *gin.Context, a *Account) error {
			_, err := s.handleAnthropicBufferedStreamingResponse(r, c, a, model, model, model, time.Now())
			return err
		}},
	}
	for _, h := range handlers {
		for _, scenario := range []string{"delivered", "write_failed", "metadata_only", "error_only"} {
			if h.plainJSON && scenario == "metadata_only" {
				continue
			}
			t.Run(h.name+"/"+scenario, func(t *testing.T) {
				var writer http.ResponseWriter = httptest.NewRecorder()
				if scenario == "write_failed" {
					writer = &codexStateDeliveryFailWriter{header: make(http.Header)}
				}
				c, _ := gin.CreateTestContext(writer)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
				cfg := &config.Config{}
				gateway := &OpenAIGatewayService{cfg: cfg, cache: &stubGatewayCache{}}
				account := &Account{ID: 918, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
				body, contentType := successSSE, "text/event-stream"
				if h.plainJSON {
					body, contentType = responseJSON, "application/json"
				}
				if scenario == "metadata_only" {
					body = created + metadata
				} else if scenario == "error_only" {
					body = failedSSE
					if h.plainJSON {
						body = failedJSON
					}
				}
				collector := &codexTurnStateHTTPCollector{service: &CodexTurnStateService{now: time.Now}, attempt: &CodexTurnStateAttempt{}}
				request := httptest.NewRequest(http.MethodPost, "/responses", nil)
				request = request.WithContext(context.WithValue(request.Context(), codexTurnStateHTTPResponseKey{}, collector))
				resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(body)), Request: request}
				err := h.handle(gateway, resp, c, account)
				if scenario == "delivered" {
					require.NoError(t, err)
				}
				collector.mu.Lock()
				delivered := collector.delivered
				collector.mu.Unlock()
				require.Equal(t, scenario == "delivered", delivered)
			})
		}
	}
}

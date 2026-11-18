//go:build unit

package service

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
)

// Keep this fixture compatible with the pre-merge implementation for comparison.
func BenchmarkRawChatCompletionsFlushBatch(b *testing.B) {
	gin.SetMode(gin.TestMode)
	for _, events := range []int{1, 128} {
		b.Run(fmt.Sprint(events), func(b *testing.B) {
			const frame = "data: {\"id\":\"chatcmpl_bench\",\"object\":\"chat.completion.chunk\",\"model\":\"model\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"}}]}\n\n"
			payload := strings.Repeat(frame, events) + "data: [DONE]\n\n"
			svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}}
			account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
			var flushes int64
			b.ReportAllocs()
			b.SetBytes(int64(len(payload)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				w := &rawStreamBenchmarkWriter{header: make(http.Header)}
				c, _ := gin.CreateTestContext(w)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
				resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(payload))}
				_, err := svc.streamRawChatCompletions(c, resp, account, "model", "model", "model", nil, nil, time.Now(), 0)
				if err != nil {
					b.Fatal(err)
				}
				flushes += w.flushes
			}
			b.ReportMetric(float64(flushes)/float64(b.N), "flushes/op")
		})
	}
}

type rawStreamBenchmarkWriter struct {
	header  http.Header
	flushes int64
}

func (w *rawStreamBenchmarkWriter) Header() http.Header         { return w.header }
func (w *rawStreamBenchmarkWriter) Write(p []byte) (int, error) { return len(p), nil }
func (w *rawStreamBenchmarkWriter) WriteHeader(int)             {}
func (w *rawStreamBenchmarkWriter) Flush()                      { w.flushes++ }

// Exercise the production entry points rather than the new editing helper so
// the exact same fixture can run on the pre-merge implementation.
func BenchmarkOpenAIReasoningRewrite300(b *testing.B) {
	filler := strings.Repeat("lorem ipsum dolor sit amet ", 40)
	var conversation strings.Builder
	conversation.WriteString(`{"model":"deepseek-chat","stream":true,"messages":[`)
	for i := 0; i < 300; i++ {
		if i > 0 {
			conversation.WriteByte(',')
		}
		fmt.Fprintf(&conversation, `{"role":"user","content":"step %d %s"},`, i, filler)
		fmt.Fprintf(&conversation, `{"role":"assistant","content":"","tool_calls":[{"id":"call_%d","type":"function","function":{"name":"exec","arguments":"{\"cmd\":\"%s\"}"}}]},`, i, filler)
		fmt.Fprintf(&conversation, `{"role":"tool","tool_call_id":"call_%d","content":"%s"}`, i, filler)
	}
	conversation.WriteString(`]}`)
	deepSeekBody := []byte(conversation.String())
	ollamaBody := []byte(strings.ReplaceAll(conversation.String(), `"role":"assistant"`, `"role":"assistant","reasoning_content":"existing reasoning"`))
	account := openaiPlatformDeepSeekAccount()
	for _, test := range []struct {
		name    string
		body    []byte
		rewrite func([]byte) []byte
	}{
		{"deepseek", deepSeekBody, func(body []byte) []byte { return ensureDeepSeekChatReasoningPlaceholders(account, body) }},
		{"ollama", ollamaBody, normalizeOllamaCloudChatCompletionsRequest},
	} {
		b.Run(test.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(test.body)))
			for i := 0; i < b.N; i++ {
				if len(test.rewrite(test.body)) <= len(test.body) {
					b.Fatal("fixture did not exercise reasoning insertion")
				}
			}
		})
	}
}

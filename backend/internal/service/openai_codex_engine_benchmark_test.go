package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// These fixtures intentionally use the public relay entry point and no new
// parser APIs, so the same benchmark can run against the pre-optimization tree.
// Payloads are synthetic; the writer discards output instead of retaining a
// second complete response in httptest.ResponseRecorder.
func BenchmarkCodexEngineRelay(b *testing.B) {
	gin.SetMode(gin.TestMode)
	delta := "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"synthetic text\"}\n\n"
	terminal := "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_fixture\",\"model\":\"fixture-model\",\"usage\":{\"input_tokens\":11,\"output_tokens\":7}}}\n\n"
	multiline := "event: response.output_text.delta\r\ndata: {\"type\":\"response.output_text.delta\",\r\ndata: \"delta\":\"synthetic text\"}\r\n\r\n"
	fixtures := []struct {
		name  string
		wire  string
		delay time.Duration
	}{
		{name: "small_256", wire: strings.Repeat(delta, 256) + terminal},
		{name: "multiline_128", wire: strings.Repeat(multiline, 128) + terminal},
		{name: "slow_write_32", wire: strings.Repeat(delta, 32) + terminal, delay: 100 * time.Microsecond},
	}
	for _, mib := range []int{1, 8, 32} {
		wire := strings.Repeat(delta, 4) + "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_fixture\",\"output\":[{\"type\":\"image_generation_call\",\"id\":\"image_fixture\",\"size\":\"1024x1024\",\"result\":\"" + strings.Repeat("A", mib<<20) + "\"}],\"usage\":{\"input_tokens\":11,\"output_tokens\":7}}}\n\n"
		fixtures = append(fixtures, struct {
			name  string
			wire  string
			delay time.Duration
		}{name: fmt.Sprintf("terminal_%dMiB", mib), wire: wire})
	}
	for _, fixture := range fixtures {
		for _, websocket := range []bool{false, true} {
			transport := "HTTP"
			if websocket {
				transport = "WS"
			}
			b.Run(transport+"/"+fixture.name, func(b *testing.B) {
				wire := []byte(fixture.wire)
				svc := &OpenAIGatewayService{}
				var firstWriteTotal, writeTotal time.Duration
				var latencies [1024]int64
				samples := 0
				b.SetBytes(int64(len(wire)))
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					ctx, cancel := context.WithCancelCause(context.Background())
					c, _ := gin.CreateTestContext(httptest.NewRecorder())
					writer := &codexEngineBenchmarkWriter{ResponseWriter: c.Writer, delay: fixture.delay}
					c.Writer = writer
					if websocket {
						c.Writer = &codexEngineWSWriter{ResponseWriter: writer, header: http.Header{"Content-Type": {"text/event-stream"}}, status: http.StatusOK, write: func(payload []byte) error {
							_, err := writer.Write(payload)
							return err
						}}
					}
					body := io.NopCloser(bytes.NewReader(wire))
					resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: body}
					result := &OpenAIForwardResult{Stream: true, UpstreamEndpoint: "/v1/responses"}
					started := time.Now()
					err := svc.relayCodexEngineStream(ctx, ctx, c, resp, result, nil, started, false, cancel)
					if samples < len(latencies) {
						latencies[samples] = time.Since(started).Nanoseconds()
						samples++
					}
					cancel(nil)
					_ = body.Close()
					if err != nil {
						b.Fatal(err)
					}
					if result.Usage.InputTokens != 11 || result.Usage.OutputTokens != 7 || writer.first.IsZero() {
						b.Fatal("fixture did not reach its terminal observation and writer")
					}
					firstWriteTotal += writer.first.Sub(started)
					writeTotal += writer.writeTime
				}
				b.StopTimer()
				slices.Sort(latencies[:samples])
				if samples > 0 {
					b.ReportMetric(float64(latencies[(samples*95+99)/100-1]), "p95-ns")
				}
				b.ReportMetric(float64(firstWriteTotal.Nanoseconds())/float64(b.N), "first-write-ns/op")
				b.ReportMetric(float64(writeTotal.Nanoseconds())/float64(b.N), "write-ns/op")
			})
		}
	}
}

type codexEngineBenchmarkWriter struct {
	gin.ResponseWriter
	delay     time.Duration
	first     time.Time
	writeTime time.Duration
}

func (w *codexEngineBenchmarkWriter) Write(payload []byte) (int, error) {
	started := time.Now()
	if w.delay > 0 {
		time.Sleep(w.delay)
	}
	if w.first.IsZero() {
		w.first = time.Now()
	}
	w.writeTime += time.Since(started)
	return len(payload), nil
}

func (w *codexEngineBenchmarkWriter) FlushError() error { return nil }

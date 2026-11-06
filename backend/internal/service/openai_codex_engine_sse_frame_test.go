package service

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/runtimeobs"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type codexEngineChunkReader struct {
	io.Reader
	chunk int
}

func (r codexEngineChunkReader) Read(p []byte) (int, error) {
	return r.Reader.Read(p[:min(len(p), r.chunk)])
}

func TestCodexEngineSSEFrameViewsPreserveBytesAndEmptyData(t *testing.T) {
	for _, fixture := range []struct {
		name, wire, payload, event string
		multi                      bool
	}{
		{"single", ": heartbeat\r\nid: 9\r\nretry: 100\r\nunknown: opaque\r\nevent: response.done\r\ndata: {\"type\":\"response.done\"}\r\n\r\n", `{"type":"response.done"}`, "response.done", false},
		{"multi", "event: first\nevent: last\ndata: {\ndata: \"fixture\":true}\n\n", "{\n\"fixture\":true}", "last", true},
		{"empty_leading", "data:\ndata: \ndata: {}\n\n", "{}", "", true},
		{"empty_trailing", "data: {}\ndata:\n\n", "{}\n", "", true},
		{"all_empty", "data:\ndata: \n\n", "", "", true},
		{"two_spaces", "data:  {}\n\n", " {}", "", false},
		{"comment", ": keepalive\n\n", "", "", false},
		{"eof_tail", "event: response.done\ndata: {}", "{}", "response.done", false},
	} {
		for _, chunk := range []int{1, 7, 4096} {
			t.Run(fixture.name+"/"+strconv.Itoa(chunk), func(t *testing.T) {
				reader := &codexEngineSSEReader{}
				frame := &codexEngineSSEFrame{}
				reader.readFrame(bufio.NewReader(codexEngineChunkReader{strings.NewReader(fixture.wire), chunk}), frame, 1<<20)
				require.False(t, frame.oversized)
				require.Equal(t, fixture.wire, string(frame.raw))
				payload := frame.payload()
				require.Equal(t, fixture.payload, string(payload))
				require.Equal(t, fixture.event, frame.eventName())
				if !fixture.multi {
					require.Nil(t, frame.scratch)
					if len(payload) > 0 {
						require.Same(t, &frame.raw[frame.firstData.start], &payload[0])
					}
				}
			})
		}
	}
}

func TestCodexEngineSSEFrameGrowthAndRetention(t *testing.T) {
	wire := "data: first\ndata: " + strings.Repeat("A", 128<<10) + "\n\n"
	frame := &codexEngineSSEFrame{}
	reader := &codexEngineSSEReader{}
	reader.readFrame(bufio.NewReader(strings.NewReader(wire)), frame, len(wire))
	require.NoError(t, frame.err)
	require.Equal(t, "first\n"+strings.Repeat("A", 128<<10), string(frame.payload()))
	require.Greater(t, cap(frame.raw), codexEngineRetainedSSEBuffer)
	require.Greater(t, cap(frame.scratch), codexEngineRetainedSSEBuffer)
	frame.reset()
	require.Nil(t, frame.raw)
	require.Nil(t, frame.scratch)
	reader.readFrame(bufio.NewReader(strings.NewReader("data: small\n\n")), frame, 64)
	require.Equal(t, "small", string(frame.payload()))
	capacity := cap(frame.raw)
	frame.reset()
	require.Equal(t, capacity, cap(frame.raw))
	reader.readFrame(bufio.NewReader(strings.NewReader(wire)), frame, len(wire)-1)
	require.True(t, frame.oversized)
	require.ErrorContains(t, frame.err, "event exceeds configured limit")
}

func TestCodexEngineSSEBufferGaugeTracksCapacityAndRelease(t *testing.T) {
	var metrics runtimeobs.Counters
	frame := &codexEngineSSEFrame{bufferGauge: metrics.TrackSSEBuffers()}
	defer frame.bufferGauge.Release()
	reader := &codexEngineSSEReader{}
	wire := "data: first\ndata: " + strings.Repeat("A", 128<<10) + "\n\n"
	reader.readFrame(bufio.NewReader(strings.NewReader(wire)), frame, len(wire))
	snapshot := metrics.Snapshot()
	require.Equal(t, int64(2), snapshot.ActiveSSEBuffers)
	require.Equal(t, int64(cap(frame.raw)+cap(frame.scratch)), snapshot.ActiveSSEBufferBytes)
	frame.reset()
	require.Zero(t, metrics.Snapshot().ActiveSSEBufferBytes)
	require.Zero(t, metrics.Snapshot().ActiveSSEBuffers)
	reader.readFrame(bufio.NewReader(strings.NewReader("data: small\n\n")), frame, 64)
	frame.reset()
	require.Equal(t, int64(cap(frame.raw)), metrics.Snapshot().ActiveSSEBufferBytes)
	frame.bufferGauge.Release()
	require.Zero(t, metrics.Snapshot().ActiveSSEBufferBytes)
	require.Zero(t, metrics.Snapshot().ActiveSSEBuffers)
}

func TestCodexEngineSSEReaderHasTwoLeasesAndStopsBlockedRead(t *testing.T) {
	before := runtimeobs.Default.Snapshot()
	reader := newCodexEngineSSEReader(io.NopCloser(strings.NewReader("data: first\n\ndata: second\n\ndata: third\n\n")), 1024, false)
	defer reader.stop()
	first, second := <-reader.frames, <-reader.frames
	require.NotSame(t, first, second)
	require.Empty(t, reader.available)
	require.Equal(t, "first", string(first.payload()))
	reader.release(second)
	third := <-reader.frames
	require.Same(t, second, third)
	require.Equal(t, "third", string(third.payload()))
	require.Equal(t, "first", string(first.payload()), "an outstanding writer lease must remain immutable")
	reader.release(first)
	reader.release(third)
	reader.stop()
	select {
	case <-reader.finished:
	default:
		t.Fatal("stop must join the producer")
	}
	pipe, writer := io.Pipe()
	defer writer.Close()
	blocked := newCodexEngineSSEReader(pipe, 1024, true)
	stopped := make(chan struct{})
	go func() { blocked.stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("stop did not interrupt pending body read")
	}
	after := runtimeobs.Default.Snapshot()
	require.Equal(t, before.ActiveSSEBuffers, after.ActiveSSEBuffers)
	require.Equal(t, before.ActiveSSEBufferBytes, after.ActiveSSEBufferBytes)
}

func TestCodexEngineSSEMultilineActivityKeepsIdleDeadline(t *testing.T) {
	pipe, writer := io.Pipe()
	defer writer.Close()
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	writeDone := make(chan error, 1)
	go func() {
		for _, line := range []string{"event: response.completed\n", "data: {\n", "data: \"type\":\"response.completed\"}\n", "\n"} {
			if _, err := io.WriteString(writer, line); err != nil {
				writeDone <- err
				return
			}
			time.Sleep(400 * time.Millisecond)
		}
		writeDone <- nil
	}()
	svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{StreamDataIntervalTimeout: 1}}}
	err := svc.relayCodexEngineStream(ctx, ctx, c, &http.Response{StatusCode: 200, Body: pipe}, &OpenAIForwardResult{}, nil, time.Now(), false, cancel)
	require.NoError(t, err, "complete lines must keep a >1s unfinished event alive")
	require.NoError(t, <-writeDone)
}

func TestCodexEngineWSBorrowedViewMatchesWriter(t *testing.T) {
	for _, wire := range []string{
		": heartbeat\n\n", "data: [DONE]\n\n", "data:\ndata: {}\ndata:\n\n",
		"event: response.failed\r\ndata: {\"error\":{\"code\":\"fixture\"},\r\ndata: \"status_code\":403}\r\n\r\n",
		"event: response.done\ndata: {\"type\":\"response.completed\",\"unknown\":9007199254740993123}\n\n",
	} {
		var original, borrowed []byte
		oldWriter := &codexEngineWSWriter{header: http.Header{"Content-Type": {"text/event-stream"}}, status: 200, write: func(data []byte) error { original = bytes.Clone(data); return nil }}
		viewWriter := &codexEngineWSWriter{header: http.Header{"Content-Type": {"text/event-stream"}}, status: 200, write: func(data []byte) error { borrowed = bytes.Clone(data); return nil }}
		frame := &codexEngineSSEFrame{}
		(&codexEngineSSEReader{}).readFrame(bufio.NewReader(strings.NewReader(wire)), frame, 1024)
		n, err := oldWriter.Write([]byte(wire))
		require.NoError(t, err)
		viewN, viewErr := viewWriter.writeSSEFrame(frame.raw, frame.payload(), frame.eventName())
		require.NoError(t, viewErr)
		require.Equal(t, n, viewN)
		require.Equal(t, original, borrowed)
		require.Equal(t, oldWriter.sent, viewWriter.sent)
	}
}

func TestCodexEngineImageObserverMatchesExistingAccounting(t *testing.T) {
	old, current := &OpenAIForwardResult{}, &OpenAIForwardResult{}
	oldStream, currentStream := newOpenAIImageOutputCounter(), newOpenAIImageOutputCounter()
	for _, data := range []string{
		`{"type":"response.output_text.delta","delta":"fixture"}`,
		`{"data":[{"url":"fixture://one","size":"1024x1024"}]}`,
		`{"d\u0061ta":[{"url":"fixture://escaped","size":"512x512"}]}`,
		`{"type":"response.output_item.done","item":{"type":"image_generation_call","id":"one","result":"AAAA","size":"1024x1024"}}`,
		`{"type":"response.output_item.done","item":{"type":"image_generation_call","result":"BBBB"}}`,
		`{"type":"response.completed","response":{"output":[{"type":"image_generation_call","id":"one","result":"AAAA"},{"type":"image_generation_call","result":"BBBB","size":"512x512"}]}}`,
		`{"usage":{"images":5}}`,
		`{"tool_usage":{"image_gen":{"images":6}}}`,
		`{"type":"image_generation.completed","b64_json":""}`,
		`{"type":"image_edit.completed","url":"fixture://new","size":"512x512"}`,
		`{"data":[{"b64_json":"CCCC"}],"output":[{"type":"image_generation_call","id":"ignored","partial_image":"yes","result":"DDDD"}]}`,
		`{"data":`, `[DONE]`,
	} {
		payload := []byte(data)
		if sizes := collectOpenAIResponseImageOutputSizesFromJSONBytes(payload); len(sizes) > 0 {
			old.ImageOutputSizes = sizes
		}
		old.ImageCount = max(old.ImageCount, extractOpenAIImagesBillableCountFromJSONBytes(payload))
		oldStream.AddSSEData(payload)
		old.ImageCount = max(old.ImageCount, oldStream.Count())
		if sizes := oldStream.Sizes(); len(sizes) > 0 {
			old.ImageOutputSizes = sizes
		}
		observeCodexEngineImages(current, payload, currentStream)
		require.Equal(t, old.ImageCount, current.ImageCount, data)
		require.Equal(t, old.ImageOutputSizes, current.ImageOutputSizes, data)
	}
}

func TestCodexEngineImageObservationSharesAuthoritativeReceipt(t *testing.T) {
	result := &OpenAIForwardResult{Stream: true, UpstreamEndpoint: "/v1/responses", UpstreamHeaders: http.Header{codexEngineUsageIDHeader: {"ceu_" + strings.Repeat("a", 43)}}, Usage: OpenAIUsage{InputTokens: 99, OutputTokens: 8}}
	observeCodexEnginePayload(result, []byte(`{"type":"response.completed","response":{"output":[{"type":"image_generation_call","id":"fixture","size":"512x512","result":"AAAA"}],"usage":{"input_tokens":0,"output_tokens":0}}}`), "response.completed")
	require.True(t, result.CodexEngineUsageFinalized)
	require.Zero(t, result.Usage.InputTokens)
	require.Zero(t, result.Usage.OutputTokens)
	require.Equal(t, 1, result.ImageCount)
	require.Equal(t, []string{"512x512"}, result.ImageOutputSizes)
}

func TestCodexEngineObserverFastPathsPreserveSemantics(t *testing.T) {
	for _, raw := range []string{"fixture", "partial_image", "PARTIAL_IMAGE", "ppartial_image", "partpartial_image", "partxial_image", "PARTİAL_IMAGE", "图像partial_image", strings.Repeat("A", 1<<20)} {
		require.Equal(t, strings.Contains(strings.ToLower(raw), "partial_image"), codexEngineContainsPartialImage(raw), raw[:min(len(raw), 50)])
	}
	for _, event := range []string{"", "response.created", "response.failed", "response.output_text.delta", "error", " ping "} {
		for _, data := range []string{"", " [DONE]\n", `{"type":"response.created"}`, `{"type":"response.failed","error":{"code":"fixture","message":"fixture"}}`, `{"type":"response.output_text.delta","delta":"fixture"}`} {
			require.Equal(t, openAIStreamDataStartsSemanticTTFT(data, event), codexEngineStreamStartsSemanticTTFT([]byte(data), event), event+data)
		}
	}
}

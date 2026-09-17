package codexnative

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"io"
	"net/http"
	"testing"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
)

type closeCountingBody struct {
	io.Reader
	closed int
}

func (b *closeCountingBody) Close() error { b.closed++; return nil }

func TestNativeResponseDecompression(t *testing.T) {
	for _, encoding := range []string{"gzip", "deflate", "br", "zstd", "unknown"} {
		t.Run(encoding, func(t *testing.T) {
			const content = "data: {\"delta\":\"text\"}\n\n"
			var compressed bytes.Buffer
			var writer io.WriteCloser
			switch encoding {
			case "gzip":
				writer = gzip.NewWriter(&compressed)
			case "deflate":
				writer, _ = flate.NewWriter(&compressed, flate.DefaultCompression)
			case "br":
				writer = brotli.NewWriter(&compressed)
			case "zstd":
				writer, _ = zstd.NewWriter(&compressed)
			default:
				compressed.WriteString(content)
			}
			if writer != nil {
				_, _ = io.WriteString(writer, content)
				_ = writer.Close()
			}
			source := &closeCountingBody{Reader: bytes.NewReader(compressed.Bytes())}
			response := &http.Response{Header: http.Header{"Content-Encoding": {encoding}, "Content-Length": {"100"}}, Body: source, ContentLength: 100}
			decodeResponse(response)
			body, err := io.ReadAll(response.Body)
			if err != nil || string(body) != content {
				t.Fatalf("body %q, err %v", body, err)
			}
			if err := response.Body.Close(); err != nil || source.closed != 1 {
				t.Fatalf("source not closed: %d %v", source.closed, err)
			}
			if encoding != "unknown" && (!response.Uncompressed || response.ContentLength != -1 || response.Header.Get("Content-Encoding") != "") {
				t.Fatalf("decoded response metadata: %+v", response)
			}
			if encoding == "unknown" && (response.Uncompressed || response.Body != source) {
				t.Fatal("unknown encoding was modified")
			}
		})
	}
}

func TestNativeDecompressionIsLazyAndClosesUnconsumedBody(t *testing.T) {
	body := &closeCountingBody{Reader: bytes.NewReader([]byte("invalid gzip"))}
	response := &http.Response{Header: http.Header{"Content-Encoding": {"gzip"}}, Body: body}
	decodeResponse(response)
	if body.closed != 0 {
		t.Fatal("decoder consumed or closed response before caller")
	}
	_ = response.Body.Close()
	if body.closed != 1 {
		t.Fatal("unconsumed response leaked")
	}
}

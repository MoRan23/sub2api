package codexnative

import (
	"compress/flate"
	"compress/gzip"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
)

// decodeResponse preserves req auxiliary clients' lazy, streaming decompression
// without its Accept-Encoding advertisement or mutation of the request.
func decodeResponse(response *http.Response) {
	if response.Body == nil || response.Body == http.NoBody || response.Uncompressed {
		return
	}
	encoding := strings.ToLower(strings.TrimSpace(response.Header.Get("Content-Encoding")))
	switch encoding {
	case "gzip", "deflate", "br", "zstd":
		response.Body = &decodedBody{source: response.Body, encoding: encoding}
		response.Header.Del("Content-Encoding")
		response.Header.Del("Content-Length")
		response.ContentLength = -1
		response.Uncompressed = true
	}
}

type decodedBody struct {
	source    io.ReadCloser
	encoding  string
	reader    io.Reader
	close     func()
	err       error
	readMu    sync.Mutex
	closeOnce sync.Once
	closed    atomic.Bool
	closeErr  error
}

func (b *decodedBody) Read(buffer []byte) (int, error) {
	b.readMu.Lock()
	defer b.readMu.Unlock()
	if b.closed.Load() {
		return 0, http.ErrBodyReadAfterClose
	}
	if b.err != nil {
		return 0, b.err
	}
	if b.reader == nil {
		switch b.encoding {
		case "gzip":
			var reader *gzip.Reader
			reader, b.err = gzip.NewReader(b.source)
			if b.err == nil {
				b.reader, b.close = reader, func() { _ = reader.Close() }
			}
		case "deflate":
			reader := flate.NewReader(b.source)
			b.reader, b.close = reader, func() { _ = reader.Close() }
		case "br":
			b.reader = brotli.NewReader(b.source)
		case "zstd":
			var reader *zstd.Decoder
			reader, b.err = zstd.NewReader(b.source)
			if b.err == nil {
				b.reader, b.close = reader, reader.Close
			}
		}
		if b.err != nil {
			return 0, b.err
		}
	}
	return b.reader.Read(buffer)
}

func (b *decodedBody) Close() error {
	// Close the source before waiting for an active Read, so cancellation can
	// interrupt a decoder waiting for more compressed bytes. Decoder cleanup
	// itself must not race Read (notably zstd's pooled decoder state).
	b.closeOnce.Do(func() {
		b.closed.Store(true)
		b.closeErr = b.source.Close()
	})
	b.readMu.Lock()
	defer b.readMu.Unlock()
	if b.close != nil {
		b.close()
		b.close = nil
	}
	return b.closeErr
}

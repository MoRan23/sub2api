package codexnative

import (
	"bytes"
	"crypto/tls"
	"net"
	"net/http"
	"testing"
	"time"
)

type partialWriteConn struct {
	net.Conn
	wire  bytes.Buffer
	limit int
}

func (c *partialWriteConn) Write(data []byte) (int, error) {
	if len(data) > c.limit {
		data = data[:c.limit]
	}
	return c.wire.Write(data)
}

func TestFirstRecordVersionOnlyRewritesFirstHeader(t *testing.T) {
	for _, limit := range []int{1, 2, 4, 100} {
		raw := &partialWriteConn{limit: limit}
		conn := &firstRecordVersionConn{Conn: raw}
		original := []byte{22, 3, 1, 0, 4, 1, 2, 3, 4, 22, 3, 1, 0, 1, 7}
		remaining := append([]byte(nil), original...)
		for len(remaining) > 0 {
			n, err := conn.Write(remaining)
			if err != nil || n <= 0 {
				t.Fatalf("write: %d %v", n, err)
			}
			remaining = remaining[n:]
		}
		expected := append([]byte(nil), original...)
		expected[2] = 3
		if !bytes.Equal(raw.wire.Bytes(), expected) {
			t.Fatalf("limit=%d got=%x want=%x", limit, raw.wire.Bytes(), expected)
		}
		if original[2] != 1 {
			t.Fatal("caller buffer was modified")
		}
	}
}

func TestNativeDecompressionCloseInterruptsBlockedRead(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	body := &decodedBody{source: client, encoding: "gzip"}
	done := make(chan error, 1)
	go func() { _, err := body.Read(make([]byte, 100)); done <- err }()
	if err := body.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("blocked read succeeded after close")
		}
	case <-time.After(time.Second):
		t.Fatal("decoder close did not interrupt read")
	}
}

func TestNativeTransportRejectsConflictingTLSPolicy(t *testing.T) {
	for _, config := range []*tls.Config{
		{MinVersion: tls.VersionTLS13},
		{MaxVersion: tls.VersionTLS12},
	} {
		if _, err := NewTransport(Linux, &http.Transport{TLSClientConfig: config}); err == nil {
			t.Fatal("silently ignored an explicit TLS version restriction")
		}
	}
}

func TestOutboundUserAgentUsesDeterministicHeaderSemantics(t *testing.T) {
	for _, test := range []struct {
		header   http.Header
		expected string
	}{
		{http.Header{"user-agent": {"Windows"}}, "Windows"},
		{http.Header{"User-Agent": {"Linux"}, "user-agent": {"Windows"}}, "Linux"},
		{http.Header{"User-Agent": {""}, "user-agent": {"Windows"}}, ""},
		{nil, ""},
	} {
		if got := UserAgent(test.header); got != test.expected {
			t.Fatalf("UA = %q, want %q", got, test.expected)
		}
	}
}

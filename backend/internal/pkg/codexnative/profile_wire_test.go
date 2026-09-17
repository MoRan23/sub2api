package codexnative

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"reflect"
	"testing"
	"time"
)

// These fixtures retain only the stable, public ClientHello fields from the
// native Codex captures. Hostnames, randoms, session IDs and key material are
// deliberately absent; the tests use fresh connections to a local listener.
const nativeWireFixtures = `[
  {
    "platform":"windows", "record_version":771, "legacy_version":771, "session_id_length":0,
    "cipher_suites":[49196,49195,49200,49199,49188,49187,49192,49191,49162,49161,49172,49171,157,156,61,60,53,47],
    "compression_methods":[0],
    "extensions":[
      {"id":0},
      {"id":10,"payload_hex":"0006001d00170018"},
      {"id":11,"payload_hex":"0100"},
      {"id":13,"payload_hex":"0018080408050806040105010201040305030203020206010603"},
      {"id":35,"payload_hex":""},
      {"id":23,"payload_hex":""},
      {"id":65281,"payload_hex":"00"}
    ]
  },
  {
    "platform":"macos", "record_version":769, "legacy_version":771, "session_id_length":0,
    "cipher_suites":[255,49196,49195,49188,49187,49162,49161,49160,49200,49199,49192,49191,49172,49171,49170,157,156,61,60,53,47,10],
    "compression_methods":[0],
    "extensions":[
      {"id":0},
      {"id":10,"payload_hex":"0006001700180019"},
      {"id":11,"payload_hex":"0100"},
      {"id":13,"payload_hex":"001004010201050106010403020305030603"},
      {"id":5,"payload_hex":"0100000000"},
      {"id":18,"payload_hex":""},
      {"id":23,"payload_hex":""}
    ]
  },
  {
    "platform":"linux", "record_version":769, "legacy_version":771, "session_id_length":32,
    "cipher_suites":[4866,4867,4865,49196,49200,159,52393,52392,52394,49195,49199,158,49188,49192,107,49187,49191,103,49162,49172,57,49161,49171,51,157,156,61,60,53,47],
    "compression_methods":[0],
    "extensions":[
      {"id":65281,"payload_hex":"00"},
      {"id":0},
      {"id":11,"payload_hex":"0100"},
      {"id":10,"payload_hex":"001011ec001d0017001e0018001901000101"},
      {"id":35,"payload_hex":""},
      {"id":22,"payload_hex":""},
      {"id":23,"payload_hex":""},
      {"id":13,"payload_hex":"003409050906090404030503060308070808081a081b081c0809080a080b080408050806040105010601030303010302040205020602"},
      {"id":43,"payload_hex":"0403040303"},
      {"id":45,"payload_hex":"0101"},
      {"id":51,"length":1258}
    ],
    "key_shares":[{"group":4588,"length":1216},{"group":29,"length":32}]
  }
]`

type nativeWireFixture struct {
	Platform           string   `json:"platform"`
	RecordVersion      uint16   `json:"record_version"`
	LegacyVersion      uint16   `json:"legacy_version"`
	SessionIDLength    int      `json:"session_id_length"`
	CipherSuites       []uint16 `json:"cipher_suites"`
	CompressionMethods []byte   `json:"compression_methods"`
	Extensions         []struct {
		ID         uint16 `json:"id"`
		PayloadHex string `json:"payload_hex"`
		Length     int    `json:"length"`
	} `json:"extensions"`
	KeyShares []struct {
		Group  uint16 `json:"group"`
		Length int    `json:"length"`
	} `json:"key_shares"`
}

type nativeWireExtension struct {
	id      uint16
	payload []byte
}

type nativeWireHello struct {
	recordVersion uint16
	legacyVersion uint16
	random        []byte
	sessionID     []byte
	cipherSuites  []uint16
	compression   []byte
	extensions    []nativeWireExtension
	keyShares     map[uint16][]byte
}

func TestNativeClientHelloWireProfiles(t *testing.T) {
	var fixtures []nativeWireFixture
	if err := json.Unmarshal([]byte(nativeWireFixtures), &fixtures); err != nil {
		t.Fatal(err)
	}
	platforms := map[string]Platform{"windows": Windows, "macos": MacOS, "linux": Linux}
	for _, fixture := range fixtures {
		t.Run(fixture.Platform, func(t *testing.T) {
			listener, transport := newNativeWireCaptureTransport(t, platforms[fixture.Platform])
			var previous *nativeWireHello
			for _, hostname := range []string{"first.codex.invalid", "second.codex.invalid"} {
				hello := captureNativeWireHello(t, listener, transport, hostname)
				checkNativeWireHello(t, hello, fixture, hostname)
				if bytes.Equal(hello.random, make([]byte, 32)) {
					t.Error("ClientHello random is all zero")
				}
				if previous != nil {
					if bytes.Equal(previous.random, hello.random) {
						t.Error("fresh connections reused the ClientHello random")
					}
					if len(hello.sessionID) > 0 && bytes.Equal(previous.sessionID, hello.sessionID) {
						t.Error("fresh connections reused the session ID")
					}
					for group, key := range hello.keyShares {
						if bytes.Equal(previous.keyShares[group], key) {
							t.Errorf("fresh connections reused key material for group %d", group)
						}
					}
				}
				previous = hello
			}
		})
	}
}

func newNativeWireCaptureTransport(t *testing.T, platform Platform) (net.Listener, http.RoundTripper) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	base := &http.Transport{
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, listener.Addr().String())
		},
		TLSHandshakeTimeout: 3 * time.Second,
		ForceAttemptHTTP2:   true,
	}
	transport, err := NewTransport(platform, base)
	if err != nil {
		t.Fatal(err)
	}
	if closer, ok := transport.(interface{ CloseIdleConnections() }); ok {
		t.Cleanup(closer.CloseIdleConnections)
	}
	return listener, transport
}

func captureNativeWireHello(t *testing.T, listener net.Listener, transport http.RoundTripper, hostname string) *nativeWireHello {
	t.Helper()
	if err := listener.(*net.TCPListener).SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	type captureResult struct {
		record []byte
		err    error
	}
	captured := make(chan captureResult, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			captured <- captureResult{err: err}
			return
		}
		defer conn.Close()
		if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
			captured <- captureResult{err: err}
			return
		}
		header := make([]byte, 5)
		if _, err := io.ReadFull(conn, header); err != nil {
			captured <- captureResult{err: err}
			return
		}
		body := make([]byte, int(binary.BigEndian.Uint16(header[3:5])))
		_, err = io.ReadFull(conn, body)
		captured <- captureResult{record: append(header, body...), err: err}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+hostname+"/responses", bytes.NewBufferString("{}"))
	if err != nil {
		t.Fatal(err)
	}
	response, roundTripErr := transport.RoundTrip(request)
	if response != nil && response.Body != nil {
		response.Body.Close()
	}
	// The listener intentionally closes after ClientHello, without a ServerHello.
	if roundTripErr == nil {
		t.Error("RoundTrip unexpectedly succeeded against a ClientHello-only listener")
	}
	select {
	case result := <-captured:
		if result.err != nil {
			t.Fatalf("capture ClientHello: %v (RoundTrip: %v)", result.err, roundTripErr)
		}
		return parseNativeWireHello(t, result.record)
	case <-time.After(5 * time.Second):
		t.Fatalf("ClientHello was not captured (RoundTrip: %v)", roundTripErr)
		return nil
	}
}

func parseNativeWireHello(t *testing.T, record []byte) *nativeWireHello {
	t.Helper()
	if len(record) < 9 || record[0] != 22 || record[5] != 1 {
		t.Fatal("expected one TLS handshake record containing a ClientHello")
	}
	if int(binary.BigEndian.Uint16(record[3:5])) != len(record)-5 {
		t.Fatal("invalid TLS record length")
	}
	helloLength := int(record[6])<<16 | int(record[7])<<8 | int(record[8])
	if helloLength != len(record)-9 {
		t.Fatal("ClientHello must fill the first record without trailing handshake messages")
	}
	reader := bytes.NewReader(record[9:])
	hello := &nativeWireHello{
		recordVersion: binary.BigEndian.Uint16(record[1:3]),
		legacyVersion: binary.BigEndian.Uint16(readNativeWireBytes(t, reader, 2)),
		random:        readNativeWireBytes(t, reader, 32),
		keyShares:     make(map[uint16][]byte),
	}
	hello.sessionID = readNativeWireVector(t, reader, 1)
	ciphers := bytes.NewReader(readNativeWireVector(t, reader, 2))
	for ciphers.Len() > 0 {
		hello.cipherSuites = append(hello.cipherSuites, binary.BigEndian.Uint16(readNativeWireBytes(t, ciphers, 2)))
	}
	hello.compression = readNativeWireVector(t, reader, 1)
	extensions := bytes.NewReader(readNativeWireVector(t, reader, 2))
	for extensions.Len() > 0 {
		hello.extensions = append(hello.extensions, nativeWireExtension{
			id:      binary.BigEndian.Uint16(readNativeWireBytes(t, extensions, 2)),
			payload: readNativeWireVector(t, extensions, 2),
		})
	}
	if reader.Len() != 0 {
		t.Fatal("unexpected bytes after ClientHello extensions")
	}
	return hello
}

func checkNativeWireHello(t *testing.T, hello *nativeWireHello, fixture nativeWireFixture, hostname string) {
	t.Helper()
	if hello.recordVersion != fixture.RecordVersion || hello.legacyVersion != fixture.LegacyVersion {
		t.Errorf("versions = record %#04x, hello %#04x; want %#04x, %#04x", hello.recordVersion, hello.legacyVersion, fixture.RecordVersion, fixture.LegacyVersion)
	}
	if len(hello.sessionID) != fixture.SessionIDLength {
		t.Errorf("session ID length = %d, want %d", len(hello.sessionID), fixture.SessionIDLength)
	}
	if !reflect.DeepEqual(hello.cipherSuites, fixture.CipherSuites) {
		t.Errorf("cipher suite order = %v, want %v", hello.cipherSuites, fixture.CipherSuites)
	}
	if !bytes.Equal(hello.compression, fixture.CompressionMethods) {
		t.Errorf("compression methods = %v, want %v", hello.compression, fixture.CompressionMethods)
	}
	if len(hello.extensions) != len(fixture.Extensions) {
		t.Fatalf("extension count = %d, want %d", len(hello.extensions), len(fixture.Extensions))
	}
	for i, expected := range fixture.Extensions {
		actual := hello.extensions[i]
		if actual.id == 16 {
			t.Error("native profile unexpectedly advertised ALPN")
		}
		if actual.id != expected.ID {
			t.Fatalf("extension[%d] = %d, want %d", i, actual.id, expected.ID)
		}
		switch actual.id {
		case 0:
			reader := bytes.NewReader(actual.payload)
			names := bytes.NewReader(readNativeWireVector(t, reader, 2))
			if nameType := readNativeWireBytes(t, names, 1)[0]; nameType != 0 {
				t.Errorf("SNI name type = %d, want host_name", nameType)
			}
			if serverName := string(readNativeWireVector(t, names, 2)); serverName != hostname {
				t.Errorf("SNI = %q, want dynamic hostname %q", serverName, hostname)
			}
			if reader.Len() != 0 || names.Len() != 0 {
				t.Error("SNI contains trailing bytes or additional names")
			}
		case 51:
			if len(actual.payload) != expected.Length {
				t.Errorf("key_share extension length = %d, want %d", len(actual.payload), expected.Length)
			}
			reader := bytes.NewReader(actual.payload)
			shares := bytes.NewReader(readNativeWireVector(t, reader, 2))
			for _, expectedShare := range fixture.KeyShares {
				group := binary.BigEndian.Uint16(readNativeWireBytes(t, shares, 2))
				key := readNativeWireVector(t, shares, 2)
				if group != expectedShare.Group || len(key) != expectedShare.Length {
					t.Errorf("key_share = group %d, length %d; want group %d, length %d", group, len(key), expectedShare.Group, expectedShare.Length)
				}
				if bytes.Equal(key, make([]byte, len(key))) {
					t.Errorf("key_share group %d is all zero", group)
				}
				hello.keyShares[group] = key
			}
			if reader.Len() != 0 || shares.Len() != 0 {
				t.Error("key_share contains trailing bytes or additional shares")
			}
		default:
			payload, err := hex.DecodeString(expected.PayloadHex)
			if err != nil {
				t.Fatalf("invalid fixture for extension %d: %v", expected.ID, err)
			}
			if !bytes.Equal(actual.payload, payload) {
				t.Errorf("extension %d payload = %x, want %x", actual.id, actual.payload, payload)
			}
		}
	}
}

func readNativeWireBytes(t *testing.T, reader *bytes.Reader, length int) []byte {
	t.Helper()
	data := make([]byte, length)
	if _, err := io.ReadFull(reader, data); err != nil {
		t.Fatalf("truncated ClientHello field of length %d: %v", length, err)
	}
	return data
}

func readNativeWireVector(t *testing.T, reader *bytes.Reader, lengthBytes int) []byte {
	t.Helper()
	var length int
	switch lengthBytes {
	case 1:
		length = int(readNativeWireBytes(t, reader, 1)[0])
	case 2:
		length = int(binary.BigEndian.Uint16(readNativeWireBytes(t, reader, 2)))
	default:
		t.Fatalf("unsupported vector length size %d", lengthBytes)
	}
	return readNativeWireBytes(t, reader, length)
}

package req

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestLowercaseHTTP1HeaderNames(t *testing.T) {
	for _, lower := range []bool{false, true} {
		for _, ordered := range []bool{false, true} {
			name := "default"
			if lower {
				name = "lowercase"
			}
			if ordered {
				name += "_ordered"
			}
			t.Run(name, func(t *testing.T) {
				r, err := http.NewRequest(http.MethodPost, "https://example.test/responses", strings.NewReader("abc"))
				if err != nil {
					t.Fatal(err)
				}
				r.Header.Set("User-Agent", "codex-test/1")
				r.Header.Set("Authorization", "Bearer test-only")
				r.Header["X-Multi"] = []string{"first", "second"}
				r.Close = true
				if ordered {
					r.Header[HeaderOderKey] = []string{"x-multi", "authorization", "user-agent", "connection", "x-extra", "host", "content-length"}
				}
				before := r.Header.Clone()
				tr := T()
				if lower {
					tr.SetLowercaseHTTP1HeaderNames(true)
				}
				pc := &persistConn{t: tr}
				var wire bytes.Buffer
				if err := pc.writeRequest(r, &wire, false, http.Header{"X-Extra": {"generated"}}, nil); err != nil {
					t.Fatal(err)
				}
				wantHeaders := []string{
					"Host: example.test", "User-Agent: codex-test/1", "Connection: close", "Content-Length: 3",
					"Authorization: Bearer test-only", "X-Multi: first", "X-Multi: second", "X-Extra: generated",
				}
				if ordered {
					wantHeaders = []string{
						"X-Multi: first", "X-Multi: second", "Authorization: Bearer test-only", "User-Agent: codex-test/1",
						"Connection: close", "X-Extra: generated", "Host: example.test", "Content-Length: 3",
					}
				}
				if lower {
					for i, line := range wantHeaders {
						key, value, _ := strings.Cut(line, ":")
						wantHeaders[i] = strings.ToLower(key) + ":" + value
					}
				}
				want := "POST /responses HTTP/1.1\r\n" + strings.Join(wantHeaders, "\r\n") + "\r\n\r\nabc"
				if wire.String() != want {
					t.Fatalf("unexpected HTTP/1.1 wire request:\n got %q\nwant %q", wire.String(), want)
				}
				if !reflect.DeepEqual(r.Header, before) || r.Header.Get("Authorization") != "Bearer test-only" {
					t.Fatalf("wire formatting mutated semantic headers: %#v", r.Header)
				}
			})
		}
	}
}

func TestLowercaseHTTP1TrailerNames(t *testing.T) {
	for _, lower := range []bool{false, true} {
		name := "default"
		if lower {
			name = "lowercase"
		}
		t.Run(name, func(t *testing.T) {
			r, err := http.NewRequest(http.MethodPost, "https://example.test/responses", io.NopCloser(strings.NewReader("abc")))
			if err != nil {
				t.Fatal(err)
			}
			r.Header.Set("User-Agent", "")
			r.Trailer = http.Header{"X-Checksum": {"first", "second"}, "X-Final": {"done"}}
			before := r.Trailer.Clone()
			pc := &persistConn{t: T().SetLowercaseHTTP1HeaderNames(lower)}
			var wire bytes.Buffer
			if err := pc.writeRequest(r, &wire, false, nil, nil); err != nil {
				t.Fatal(err)
			}
			want := "POST /responses HTTP/1.1\r\nHost: example.test\r\nTransfer-Encoding: chunked\r\nTrailer: X-Checksum,X-Final\r\n\r\n3\r\nabc\r\n0\r\nX-Checksum: first\r\nX-Checksum: second\r\nX-Final: done\r\n\r\n"
			if lower {
				want = "POST /responses HTTP/1.1\r\nhost: example.test\r\ntransfer-encoding: chunked\r\ntrailer: x-checksum,x-final\r\n\r\n3\r\nabc\r\n0\r\nx-checksum: first\r\nx-checksum: second\r\nx-final: done\r\n\r\n"
			}
			if wire.String() != want {
				t.Fatalf("unexpected chunked request:\n got %q\nwant %q", wire.String(), want)
			}
			if !reflect.DeepEqual(r.Trailer, before) {
				t.Fatalf("wire formatting mutated trailers: %#v", r.Trailer)
			}
			parsed, err := http.ReadRequest(bufio.NewReader(bytes.NewReader(wire.Bytes())))
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(parsed.Body)
			if err != nil || string(body) != "abc" {
				t.Fatalf("body = %q, error = %v", body, err)
			}
			if !reflect.DeepEqual(parsed.Trailer, before) {
				t.Fatalf("decoded trailers = %#v, want %#v", parsed.Trailer, before)
			}
		})
	}
}

func TestLowercaseHTTP1HeaderNamesClone(t *testing.T) {
	tr := T()
	if tr.LowercaseHTTP1HeaderNames || tr.Clone().LowercaseHTTP1HeaderNames {
		t.Fatal("option must be disabled by default")
	}
	if tr.SetLowercaseHTTP1HeaderNames(true) != tr || !tr.Clone().LowercaseHTTP1HeaderNames {
		t.Fatal("setter must return receiver and Clone must preserve option")
	}
	if tr.SetLowercaseHTTP1HeaderNames(false).LowercaseHTTP1HeaderNames {
		t.Fatal("setter must support disabling the option")
	}
}

type failingHTTP1HeaderWriter struct{ err error }

func (w failingHTTP1HeaderWriter) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte(": ")) {
		return 0, w.err
	}
	return len(p), nil
}

func (w failingHTTP1HeaderWriter) WriteByte(byte) error { return w.err }

func TestLowercaseHTTP1OrderedHeaderWriteError(t *testing.T) {
	r, err := http.NewRequest(http.MethodGet, "https://example.test/", nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Header[HeaderOderKey] = []string{"host", "user-agent"}
	pc := &persistConn{t: T().SetLowercaseHTTP1HeaderNames(true)}
	want := errors.New("test write failure")
	if err := pc.writeRequest(r, failingHTTP1HeaderWriter{want}, false, nil, nil); !errors.Is(err, want) {
		t.Fatalf("write error = %v, want %v", err, want)
	}
}

type http1ContinueBody struct {
	reader *strings.Reader
	reads  int
	closed bool
}

func (b *http1ContinueBody) Read(p []byte) (int, error) {
	b.reads++
	return b.reader.Read(p)
}

func (b *http1ContinueBody) Close() error {
	b.closed = true
	return nil
}

func TestLowercaseHTTP1ExpectContinue(t *testing.T) {
	for _, lower := range []bool{false, true} {
		for _, proceed := range []bool{false, true} {
			name := "default"
			if lower {
				name = "lowercase"
			}
			if proceed {
				name += "_continue"
			} else {
				name += "_reject"
			}
			t.Run(name, func(t *testing.T) {
				body := &http1ContinueBody{reader: strings.NewReader("abc")}
				r, err := http.NewRequest(http.MethodPost, "https://example.test/responses", body)
				if err != nil {
					t.Fatal(err)
				}
				r.ContentLength = 3
				r.Header.Set("User-Agent", "")
				r.Header.Set("Expect", "100-continue")
				r.Header[HeaderOderKey] = []string{"expect", "host", "content-length"}
				pc := &persistConn{t: T().SetLowercaseHTTP1HeaderNames(lower)}
				var wire bytes.Buffer
				waited := false
				err = pc.writeRequest(r, bufio.NewWriter(&wire), false, nil, func() bool {
					waited = true
					if body.reads != 0 || !strings.HasSuffix(wire.String(), "\r\n\r\n") {
						t.Error("headers must be flushed before waiting, without reading the body")
					}
					return proceed
				})
				if err != nil {
					t.Fatal(err)
				}
				want := "POST /responses HTTP/1.1\r\nExpect: 100-continue\r\nHost: example.test\r\nContent-Length: 3\r\n\r\n"
				if lower {
					want = "POST /responses HTTP/1.1\r\nexpect: 100-continue\r\nhost: example.test\r\ncontent-length: 3\r\n\r\n"
				}
				if proceed {
					want += "abc"
				}
				if wire.String() != want || !waited || !body.closed || (!proceed && body.reads != 0) {
					t.Fatalf("wire=%q want=%q waited=%v body=%+v", wire.String(), want, waited, body)
				}
			})
		}
	}
}

func TestLowercaseHTTP1ProxyConnectUnchanged(t *testing.T) {
	for _, lower := range []bool{false, true} {
		name := "default"
		if lower {
			name = "lowercase"
		}
		t.Run(name, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = listener.Close() })
			wire := make(chan string, 1)
			serveError := make(chan error, 1)
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					serveError <- err
					return
				}
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				reader := bufio.NewReader(conn)
				var raw strings.Builder
				for {
					line, err := reader.ReadString('\n')
					if err != nil {
						serveError <- err
						return
					}
					raw.WriteString(line)
					if line == "\r\n" {
						break
					}
				}
				_, err = io.WriteString(conn, "HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
				if err != nil {
					serveError <- err
					return
				}
				wire <- raw.String()
			}()
			proxyURL := &url.URL{Scheme: "http", Host: listener.Addr().String()}
			tr := T().SetLowercaseHTTP1HeaderNames(lower).EnableForceHTTP1()
			tr.Proxy = http.ProxyURL(proxyURL)
			tr.ProxyConnectHeader = http.Header{"User-Agent": {"proxy-test"}, "X-Proxy": {"test"}}
			t.Cleanup(tr.CloseIdleConnections)
			r, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://example.test/responses", nil)
			if err != nil {
				t.Fatal(err)
			}
			client := &http.Client{Transport: tr, Timeout: 5 * time.Second}
			if _, err := client.Do(r); err == nil || !strings.Contains(err.Error(), "Forbidden") {
				t.Fatalf("expected proxy rejection, got %v", err)
			}
			select {
			case got := <-wire:
				want := "CONNECT example.test:443 HTTP/1.1\r\nHost: example.test:443\r\nUser-Agent: proxy-test\r\nX-Proxy: test\r\n\r\n"
				if got != want {
					t.Fatalf("proxy CONNECT changed: got %q want %q", got, want)
				}
			case err := <-serveError:
				t.Fatal(err)
			case <-time.After(5 * time.Second):
				t.Fatal("proxy capture timed out")
			}
		})
	}
}

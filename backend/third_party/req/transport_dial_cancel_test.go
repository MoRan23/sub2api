package req

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"testing"
	"time"
)

func TestCancelDialOnRequestCancel(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		name := "detached_default"
		if enabled {
			name = "request_cancel"
		}
		t.Run(name, func(t *testing.T) {
			tr := T().EnableForceHTTP1().SetCancelDialOnRequestCancel(enabled)
			tr.Proxy = nil
			t.Cleanup(tr.CloseIdleConnections)
			dialStarted := make(chan context.Context, 1)
			dialStopped := make(chan struct{})
			tr.DialTLSContext = func(ctx context.Context, network, address string) (net.Conn, error) {
				dialStarted <- ctx
				<-ctx.Done()
				close(dialStopped)
				return nil, ctx.Err()
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			r, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://example.test/", nil)
			if err != nil {
				t.Fatal(err)
			}
			result := make(chan error, 1)
			go func() {
				_, err := tr.RoundTrip(r)
				result <- err
			}()
			var dialCtx context.Context
			select {
			case dialCtx = <-dialStarted:
			case <-time.After(5 * time.Second):
				t.Fatal("dial did not start")
			}
			cancel()
			select {
			case err := <-result:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("RoundTrip error = %v, want context.Canceled", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("RoundTrip did not cancel")
			}
			if enabled {
				select {
				case <-dialStopped:
				case <-time.After(5 * time.Second):
					t.Fatal("pending dial did not cancel with request")
				}
			} else {
				if dialCtx.Err() != nil {
					t.Fatalf("default transport canceled detached dial: %v", dialCtx.Err())
				}
				tr.CloseIdleConnections()
				select {
				case <-dialStopped:
				case <-time.After(5 * time.Second):
					t.Fatal("cleanup did not stop detached dial")
				}
			}
		})
	}
}

func TestCancelDialOnRequestCancelClone(t *testing.T) {
	tr := T()
	if tr.CancelDialOnRequestCancel || tr.Clone().CancelDialOnRequestCancel {
		t.Fatal("option must be disabled by default")
	}
	if tr.SetCancelDialOnRequestCancel(true) != tr || !tr.Clone().CancelDialOnRequestCancel {
		t.Fatal("setter must return receiver and Clone must preserve option")
	}
}

func TestCancelDialOnRequestCancelKeepsCompletedConnection(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()
	tr := T().EnableForceHTTP1().SetCancelDialOnRequestCancel(true)
	tr.Proxy = nil
	tr.TLSClientConfig.InsecureSkipVerify = true // Local httptest certificate only.
	defer tr.CloseIdleConnections()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := tr.RoundTrip(r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, response.Body); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	cancel()
	reused := false
	ctx = httptrace.WithClientTrace(t.Context(), &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) {
		reused = info.Reused
	}})
	r, err = http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err = tr.RoundTrip(r)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if _, err := io.Copy(io.Discard, response.Body); err != nil {
		t.Fatal(err)
	}
	if !reused {
		t.Fatal("canceling a completed request discarded its established connection")
	}
}

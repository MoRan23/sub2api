package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCodexTelemetryHTTPBodySnapshotBorrowsFinalizedBytes(t *testing.T) {
	original := []byte(`{"model":"gpt-6-sol","input":"private prompt"}`)
	want := bytes.Clone(original)
	req, err := http.NewRequest(http.MethodPost, chatgptCodexURL, nil)
	require.NoError(t, err)
	setOpenAIRequestBodySnapshot(req, original)
	// The caller still owns original; observations must use the private final
	// snapshot, even if that caller later reuses its buffer.
	for i := range original {
		original[i] = 'x'
	}
	head := make([]byte, 3)
	_, err = io.ReadFull(req.Body, head)
	require.NoError(t, err)

	body, err := readOpenAIUpstreamRequestBodySnapshot(req)
	require.NoError(t, err)
	require.Equal(t, want, body)
	require.True(t, &req.Body.(*openAIRequestBodySnapshotReader).snapshot[0] == &body[0], "observation borrows the wire snapshot")
	require.Equal(t, len(body), cap(body), "borrowed view cannot append into spare capacity")
	rest, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	require.Equal(t, want, append(head, rest...), "observation must not advance the inference reader")

	// Concurrent physical attempts have independent cursors over immutable bytes.
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, readErr := readOpenAIUpstreamRequestBodySnapshot(req)
			if readErr != nil || !bytes.Equal(want, got) {
				t.Errorf("snapshot read = %q, %v", got, readErr)
			}
		}()
	}
	wg.Wait()
}

type telemetrySnapshotReader struct {
	io.Reader
	closed bool
}

func (r *telemetrySnapshotReader) Close() error { r.closed = true; return nil }

type telemetrySnapshotReadError struct{ err error }

func (r telemetrySnapshotReadError) Read([]byte) (int, error) { return 0, r.err }

func TestCodexTelemetryHTTPBodySnapshotCompatibilityAndErrors(t *testing.T) {
	readErr := errors.New("read failed")
	getErr := errors.New("get body failed")
	for _, tc := range []struct {
		name    string
		getBody func() (io.ReadCloser, error)
		want    string
		wantErr error
	}{
		{"compatibility", func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("payload")), nil }, "payload", nil},
		{"empty compatibility", func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("")), nil }, "", nil},
		{"empty snapshot", func() (io.ReadCloser, error) { return newOpenAIRequestBodySnapshotReader(nil), nil }, "", nil},
		{"read error", func() (io.ReadCloser, error) { return io.NopCloser(telemetrySnapshotReadError{readErr}), nil }, "", readErr},
		{"open error", func() (io.ReadCloser, error) { return nil, getErr }, "", getErr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var opened io.ReadCloser
			var tracked *telemetrySnapshotReader
			req := &http.Request{Body: io.NopCloser(strings.NewReader("inference"))}
			req.GetBody = func() (io.ReadCloser, error) {
				var err error
				opened, err = tc.getBody()
				if err != nil {
					return nil, err
				}
				// Preserve snapshot's concrete type for the optimized branch.
				if _, ok := opened.(*openAIRequestBodySnapshotReader); ok {
					return opened, nil
				}
				tracked = &telemetrySnapshotReader{Reader: opened}
				return tracked, nil
			}
			body, err := readOpenAIUpstreamRequestBodySnapshot(req)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.want, string(body))
			}
			if tracked != nil {
				require.True(t, tracked.closed, "compatibility reader must close on success and error")
			}
			inference, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			require.Equal(t, "inference", string(inference))
		})
	}
	for _, req := range []*http.Request{nil, {}} {
		_, err := readOpenAIUpstreamRequestBodySnapshot(req)
		require.Error(t, err)
		require.Equal(t, []byte("fallback"), openAIUpstreamRequestBodySnapshot(req, []byte("fallback")))
	}
	failed := &http.Request{GetBody: func() (io.ReadCloser, error) { return nil, getErr }}
	require.Equal(t, []byte("fallback"), openAIUpstreamRequestBodySnapshot(failed, []byte("fallback")))
	empty := &http.Request{GetBody: func() (io.ReadCloser, error) { return newOpenAIRequestBodySnapshotReader(nil), nil }}
	require.Nil(t, openAIUpstreamRequestBodySnapshot(empty, []byte("fallback")), "valid empty snapshot must not select fallback")
}

func TestCodexTelemetryHTTPBeginUsesSnapshotAndCountsEveryPhysicalAttempt(t *testing.T) {
	telemetry, _ := telemetryCaptureService(t)
	owner := osIdentityTestAccount(t, 741)
	repo := newAuthorizedOpenAIOAuthTestRepo(owner)
	account, err := ResolveOpenAIOAuthCredentialAccount(context.Background(), repo, owner, OpenAIOSWindows)
	require.NoError(t, err)
	gateway := &OpenAIGatewayService{codexTelemetry: telemetry, accountRepo: repo}
	req, err := http.NewRequest(http.MethodPost, chatgptCodexURL, nil)
	require.NoError(t, err)
	req.Header.Set("User-Agent", owner.OpenAIOAuthOSProfiles.Profiles[OpenAIOSWindows].UserAgent)
	req.Header.Set("Authorization", "Bearer fixture-token")
	req.Header.Set("Chatgpt-Account-Id", "fixture-account")
	req.Header.Set("Session_id", "018f5c3c-6e3a-7abf-8def-1234567890ae")
	req.Header.Set("Thread_id", "018f5c3c-6e3a-7abf-8def-1234567890ae")
	req = markCodexTelemetryHTTPRequest(req, context.Background())
	body := []byte(`{"model":"gpt-6-sol","input":"` + strings.Repeat("private ", 8192) + `","client_metadata":{"turn_id":"018f5c3c-6e3a-7abf-8def-1234567890af"}}`)
	setOpenAIRequestBodySnapshot(req, body)
	for i := range body {
		body[i] = 'x'
	}
	for range 3 {
		attempt := gateway.beginCodexTelemetryHTTPRequest(req, "", account)
		require.NotNil(t, attempt)
		require.Equal(t, "gpt-6-sol", attempt.profile.input.Model)
		attempt.Retry(CodexTelemetryResult{Status: "failed", DeliveryStatus: "incomplete", HTTPStatus: http.StatusTooManyRequests, FinishedAt: time.Now()})
	}
	telemetry.mu.Lock()
	attempts := telemetry.counters.Attempts
	telemetry.mu.Unlock()
	require.EqualValues(t, 3, attempts)
	// Failed replay access is not an empty request and must not create a turn.
	req.GetBody = func() (io.ReadCloser, error) { return nil, errors.New("snapshot unavailable") }
	require.Nil(t, gateway.beginCodexTelemetryHTTPRequest(req, "", account))
	telemetry.mu.Lock()
	attempts = telemetry.counters.Attempts
	telemetry.mu.Unlock()
	require.EqualValues(t, 3, attempts)
}

var telemetrySnapshotBenchmarkSink byte

func BenchmarkCodexTelemetryHTTPRequestSnapshot(b *testing.B) {
	for _, size := range []int{64 << 10, 1 << 20, 8 << 20} {
		for _, attempts := range []int{1, 5, 15} {
			for _, legacy := range []bool{true, false} {
				mode := "borrow"
				if legacy {
					mode = "readall_baseline"
				}
				b.Run(fmt.Sprintf("bytes_%d/attempts_%d/%s", size, attempts, mode), func(b *testing.B) {
					req := &http.Request{}
					setOpenAIRequestBodySnapshot(req, bytes.Repeat([]byte("x"), size))
					b.ReportAllocs()
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						for range attempts {
							var body []byte
							var err error
							if legacy {
								reader, openErr := req.GetBody()
								if openErr != nil {
									b.Fatal(openErr)
								}
								body, err = io.ReadAll(reader)
								_ = reader.Close()
							} else {
								body, err = readOpenAIUpstreamRequestBodySnapshot(req)
							}
							if err != nil {
								b.Fatal(err)
							}
							telemetrySnapshotBenchmarkSink = body[len(body)-1]
						}
					}
				})
			}
		}
	}
}

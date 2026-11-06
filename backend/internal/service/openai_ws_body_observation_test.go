package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/runtimeobs"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOpenAIWSBodyObservationPreservesFailoverLease(t *testing.T) {
	var counters runtimeobs.Counters
	tracker := runtimeobs.NewBodyTracker(&counters)
	tracker.EnsureCurrent(1 << 20)
	finishOpenAIWSObservedTurn(tracker, &UpstreamFailoverError{})
	tracker.EnsureCurrent(2 << 20)
	require.EqualValues(t, 1, counters.Snapshot().ActiveBodies)
	require.EqualValues(t, 2<<20, counters.Snapshot().ActiveBodyBytes)
	finishOpenAIWSObservedTurn(tracker, errors.New("final rejection"))
	tracker.Close()
	require.Zero(t, counters.Snapshot().ActiveBodies)
}

type bodyObservationFrameConn struct{ payload []byte }

func (c *bodyObservationFrameConn) ReadFrame(context.Context) (coderws.MessageType, []byte, error) {
	return coderws.MessageText, c.payload, nil
}
func (*bodyObservationFrameConn) WriteFrame(context.Context, coderws.MessageType, []byte) error {
	return nil
}
func (*bodyObservationFrameConn) Close() error { return nil }

func TestOpenAIWSPassthroughBodyObservationAdmissionAndFailure(t *testing.T) {
	for _, mode := range []string{"accepted", "rejected", "failover"} {
		t.Run(mode, func(t *testing.T) {
			var counters runtimeobs.Counters
			tracker := runtimeobs.NewBodyTracker(&counters)
			defer tracker.Close()
			payload := []byte(`{"type":"response.create","input":"` + strings.Repeat("x", 1<<20) + `"}`)
			conn := &openAIWSPolicyEnforcingFrameConn{inner: &bodyObservationFrameConn{payload}, bodyObservation: tracker, filter: func(_ coderws.MessageType, p []byte) ([]byte, *OpenAIFastBlockedError, error) {
				require.EqualValues(t, 1, counters.Snapshot().ActiveLargeBodies) // Includes admission/slot wait.
				switch mode {
				case "rejected":
					return p, nil, errors.New("admission denied")
				case "failover":
					return p, nil, &UpstreamFailoverError{}
				}
				return p, nil, nil
			}}
			_, _, err := conn.ReadFrame(context.Background())
			if mode == "accepted" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			if mode == "rejected" {
				require.Zero(t, counters.Snapshot().ActiveBodies)
			} else {
				require.EqualValues(t, 1, counters.Snapshot().ActiveBodies)
				tracker.EnsureCurrent(int64(len(payload)))
				require.EqualValues(t, 1, counters.Snapshot().ActiveBodies)
			}
			tracker.CompleteCurrent()
			require.Zero(t, counters.Snapshot().ActiveBodies)
		})
	}
}

func TestOpenAIWSNativeBodyObservationQueuedFrameAndDisconnect(t *testing.T) {
	var counters runtimeobs.Counters
	tracker := runtimeobs.NewBodyTracker(&counters)
	tracker.EnsureCurrent(1 << 20)
	defer tracker.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	controls := make(chan *openAIWSNativeControl, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		conn.SetReadLimit(4 << 20)
		c, _ := gin.CreateTestContext(w)
		c.Request = r
		c.Set(openAIWSBodyObservationKey, tracker)
		control := getOpenAIWSNativeControl(c, ctx, conn, time.Second, time.Second, func([]byte) error { return nil })
		controls <- control
		control.start()
		<-control.done
	}))
	defer server.Close()
	client, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	require.NoError(t, err)
	defer client.CloseNow()
	control := <-controls
	payload := []byte(`{"type":"response.create","input":"` + strings.Repeat("x", 1<<20) + `"}`)
	require.NoError(t, client.Write(ctx, coderws.MessageText, payload))
	require.Eventually(t, func() bool { return counters.Snapshot().ActiveLargeBodies == 2 }, time.Second, time.Millisecond)
	tracker.CompleteCurrent()
	_, err = control.next(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, counters.Snapshot().ActiveBodies)
	require.NoError(t, client.Write(ctx, coderws.MessageText, payload))
	require.Eventually(t, func() bool { return counters.Snapshot().ActiveLargeBodies == 2 }, time.Second, time.Millisecond)
	_ = client.CloseNow()
	select {
	case <-control.done:
	case <-time.After(time.Second):
		t.Fatal("reader did not stop")
	}
	require.EqualValues(t, 1, counters.Snapshot().ActiveBodies) // Unconsumed queue lease drained.
	tracker.Close()
	require.Zero(t, counters.Snapshot().ActiveBodies)
}

func TestCodexEngineWSBodyObservationQueuedCreateAndTerminal(t *testing.T) {
	var counters runtimeobs.Counters
	tracker := runtimeobs.NewBodyTracker(&counters)
	defer tracker.Close()
	started := make(chan struct{}, 2)
	release := make(chan struct{}, 2)
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: &engineDisconnectUpstream{do: func(r *http.Request) (*http.Response, error) {
		started <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_observed\"}}\n\n"))}, nil
	}}}
	client, done := engineWSFixture(t, svc, engineAccount(), func(c *gin.Context) *OpenAIWSIngressHooks { c.Set(openAIWSBodyObservationKey, tracker); return nil })
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	require.NoError(t, client.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"alias"}`)))
	<-started
	large := []byte(`{"type":"response.create","model":"alias","input":"` + strings.Repeat("x", 1<<20) + `"}`)
	require.NoError(t, client.Write(ctx, coderws.MessageText, large))
	require.Eventually(t, func() bool {
		return counters.Snapshot().ActiveBodies == 2 && counters.Snapshot().ActiveLargeBodies == 1
	}, time.Second, time.Millisecond)
	release <- struct{}{}
	_, _, err := client.Read(ctx)
	require.NoError(t, err)
	<-started
	require.EqualValues(t, 1, counters.Snapshot().ActiveBodies)
	require.EqualValues(t, len(large), counters.Snapshot().ActiveLargeBodyBytes)
	release <- struct{}{}
	_, _, err = client.Read(ctx)
	require.NoError(t, err)
	require.Eventually(t, func() bool { return counters.Snapshot().ActiveBodies == 0 }, time.Second, time.Millisecond)
	_ = client.CloseNow()
	require.ErrorIs(t, <-done, context.Canceled)
	require.Zero(t, counters.Snapshot().ActiveBodyBytes)
}

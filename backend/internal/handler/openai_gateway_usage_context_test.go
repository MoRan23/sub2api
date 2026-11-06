package handler

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

func TestSubmitUsageRecordTaskCopiesRequestContext(t *testing.T) {
	parent := context.WithValue(context.Background(), ctxkey.ClientRequestID, "client-request-123")
	parent = context.WithValue(parent, ctxkey.RequestID, "request-456")

	var gotClientRequestID string
	var gotRequestID string
	h := &GatewayHandler{}
	h.submitUsageRecordTask(parent, func(ctx context.Context) {
		gotClientRequestID, _ = ctx.Value(ctxkey.ClientRequestID).(string)
		gotRequestID, _ = ctx.Value(ctxkey.RequestID).(string)
	})

	require.Equal(t, "client-request-123", gotClientRequestID)
	require.Equal(t, "request-456", gotRequestID)
}

type usageRecordParentReadGuard struct {
	context.Context
	sealed    atomic.Bool
	lateReads atomic.Int64
}

func (g *usageRecordParentReadGuard) Value(key any) any {
	if g.sealed.Load() {
		g.lateReads.Add(1)
	}
	return g.Context.Value(key)
}

func TestWrapUsageRecordTaskContextDetachesAtSubmission(t *testing.T) {
	type payloadKey struct{}
	parent, cancel := context.WithCancel(context.WithValue(context.Background(), payloadKey{}, make([]byte, 1<<20)))
	defer cancel()
	guard := &usageRecordParentReadGuard{Context: context.WithValue(context.WithValue(parent, ctxkey.ClientRequestID, " client-id "), ctxkey.RequestID, " request-id ")}
	var gotClient, gotRequest string
	var leakedPayload any
	var taskErr error
	task, abandon := wrapUsageRecordTaskContext(guard, func(ctx context.Context) {
		gotClient, _ = ctx.Value(ctxkey.ClientRequestID).(string)
		gotRequest, _ = ctx.Value(ctxkey.RequestID).(string)
		leakedPayload, taskErr = ctx.Value(payloadKey{}), ctx.Err()
	})
	guard.sealed.Store(true)
	cancel()
	workerCtx, workerCancel := context.WithTimeout(context.Background(), time.Second)
	defer workerCancel()
	task(workerCtx)
	abandon()
	require.Zero(t, guard.lateReads.Load(), "the queued closure must not read its parent context")
	require.Equal(t, "client-id", gotClient)
	require.Equal(t, "request-id", gotRequest)
	require.Nil(t, leakedPayload)
	require.NoError(t, taskErr)
}

func TestOpenAISubmitUsageRecordTaskCopiesRequestContext(t *testing.T) {
	parent := context.WithValue(context.Background(), ctxkey.ClientRequestID, "openai-client-request-123")
	parent = context.WithValue(parent, ctxkey.RequestID, "openai-request-456")

	var gotClientRequestID string
	var gotRequestID string
	h := &OpenAIGatewayHandler{}
	h.submitUsageRecordTask(parent, func(ctx context.Context) {
		gotClientRequestID, _ = ctx.Value(ctxkey.ClientRequestID).(string)
		gotRequestID, _ = ctx.Value(ctxkey.RequestID).(string)
	})

	require.Equal(t, "openai-client-request-123", gotClientRequestID)
	require.Equal(t, "openai-request-456", gotRequestID)
}

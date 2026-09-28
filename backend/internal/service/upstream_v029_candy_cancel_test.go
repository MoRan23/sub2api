package service

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestUpstreamV029CandyTransportErrorsRemainIsolated(t *testing.T) {
	for _, tc := range []struct {
		name    string
		err     error
		cancel  bool
		timeout bool
		want    string
	}{
		{name: "client cancellation", err: context.Canceled, cancel: true, want: "cancelled"},
		{name: "deadline", err: context.DeadlineExceeded, timeout: true, want: "timeout"},
		{name: "durable proxy failure", err: errors.New("proxy authentication failed with synthetic private detail"), want: "upstream_failed"},
	} {
		for _, ginOnly := range []bool{false, true} {
			t.Run(tc.name+map[bool]string{false: "/context", true: "/gin context"}[ginOnly], func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if tc.timeout {
					var deadlineCancel context.CancelFunc
					ctx, deadlineCancel = context.WithDeadline(ctx, time.Now().Add(-time.Second))
					defer deadlineCancel()
				}
				if tc.cancel {
					cancel()
				}
				testCtx := withOpenAICandyTest(ctx, &openAICandyTestAttempt{})
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest("POST", "/v1/responses", nil).WithContext(testCtx)
				if !ginOnly {
					ctx = testCtx
				}
				account := &Account{ID: 9, Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true}
				gateway := &OpenAIGatewayService{}
				err := gateway.handleOpenAIUpstreamTransportError(ctx, c, account, tc.err, false)
				require.EqualError(t, err, tc.want)
				var safe CandyTestFailure
				require.ErrorAs(t, err, &safe, "the candy guard must run before the general cancellation guard")
				_, hasOpsError := c.Get(OpsUpstreamErrorsKey)
				require.False(t, hasOpsError)
				_, hasOpsMessage := c.Get(OpsUpstreamErrorMessageKey)
				require.False(t, hasOpsMessage)
				require.Equal(t, StatusActive, account.Status)
				require.True(t, account.Schedulable)
				require.Nil(t, account.TempUnschedulableUntil)
			})
		}
	}
}

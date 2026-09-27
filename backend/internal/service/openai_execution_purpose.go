package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// This purpose can only be set by an internal caller. It is never inferred from
// request headers, request bodies, or user controlled Gin keys.
type openAICandyTestContextKey struct{}

type openAICandyTestAttempt struct {
	mu           sync.Mutex
	sends        int
	actualModel  string
	actualEffort string
	validate     func(context.Context) error
}

func withOpenAICandyTest(ctx context.Context, attempt *openAICandyTestAttempt) context.Context {
	return context.WithValue(ctx, openAICandyTestContextKey{}, attempt)
}

func isOpenAICandyTest(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	_, ok := ctx.Value(openAICandyTestContextKey{}).(*openAICandyTestAttempt)
	return ok
}

// IsAccountCandyTest allows the shared HTTP transport to preserve its current
// route while avoiding health/cooldown writes from a diagnostic request.
func IsAccountCandyTest(ctx context.Context) bool { return isOpenAICandyTest(ctx) }

func isOpenAICandyTestContext(c *gin.Context) bool {
	return c != nil && c.Request != nil && isOpenAICandyTest(c.Request.Context())
}

func isOpenAICandyTestAccount(account *Account) bool {
	return account != nil && account.openAICandyTest
}

// The final transport fence prevents any compatibility branch from silently
// replaying a paid inference, even if it later acquires another retry path.
func candyTestBeforeSend(req *http.Request) (*http.Request, error) {
	if req == nil {
		return req, nil
	}
	attempt, _ := req.Context().Value(openAICandyTestContextKey{}).(*openAICandyTestAttempt)
	if attempt == nil {
		return req, nil
	}
	if err := req.Context().Err(); err != nil {
		return nil, err
	}
	if attempt.validate != nil {
		if err := attempt.validate(req.Context()); err != nil {
			return nil, err
		}
	}
	attempt.mu.Lock()
	defer attempt.mu.Unlock()
	if attempt.sends != 0 {
		return nil, candyTestError("inference_replay_disabled")
	}
	attempt.sends++
	if req.GetBody != nil {
		body, err := req.GetBody()
		if err != nil {
			return nil, candyTestError("request_invalid")
		}
		data, err := io.ReadAll(io.LimitReader(body, 4<<20))
		_ = body.Close()
		if err != nil {
			return nil, candyTestError("request_invalid")
		}
		attempt.actualModel = strings.TrimSpace(gjson.GetBytes(data, "model").String())
		attempt.actualEffort = strings.TrimSpace(gjson.GetBytes(data, "reasoning.effort").String())
		if attempt.actualEffort == "" {
			attempt.actualEffort = strings.TrimSpace(gjson.GetBytes(data, "reasoning_effort").String())
		}
	}
	return req, nil
}

type candyTestError string

func (e candyTestError) Error() string                { return string(e) }
func (e candyTestError) CandyTestFailureCode() string { return string(e) }

func safeCandyTestError(ctx context.Context, err error) error {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return candyTestError("timeout")
	}
	if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		return candyTestError("cancelled")
	}
	var safe CandyTestFailure
	if errors.As(err, &safe) {
		return candyTestError(safe.CandyTestFailureCode())
	}
	return candyTestError("upstream_failed")
}

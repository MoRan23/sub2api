package handler

import (
	"context"
	"errors"

	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
)

// authorizeNextTurn runs only for inference frames, before slots or upstream
// writes. GetByKey uses the existing invalidated L1/L2 auth cache. Its result is
// handed to the billing-group refresh, without replacing the connection's user,
// route, subscription or quota snapshots.
func (k *openAIWSTurnBillingAPIKeys) authorizeNextTurn(ctx context.Context, lookup openAIWSTurnAPIKeyLookup, turn int, conn *service.APIKey) error {
	if turn <= 1 {
		return nil // The initial turn has just passed the handshake middleware.
	}
	k.mu.Lock()
	k.admittedTurn, k.admittedKey = 0, nil
	k.mu.Unlock()
	if svc, ok := lookup.(*service.APIKeyService); ok && svc == nil {
		lookup = nil
	}
	if lookup == nil || conn == nil || conn.ID <= 0 || conn.UserID <= 0 || conn.User == nil || conn.User.ID != conn.UserID || conn.Key == "" {
		return service.NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, "websocket authorization is no longer valid, please reconnect", nil)
	}
	latest, err := lookup.GetByKey(ctx, conn.Key)
	if err != nil {
		if errors.Is(err, service.ErrAPIKeyNotFound) {
			return service.NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, "API key is no longer valid, please reconnect", nil)
		}
		return service.NewOpenAIWSClientCloseError(coderws.StatusTryAgainLater, "unable to verify websocket authorization, please reconnect", err)
	}
	if latest == nil || latest.ID != conn.ID || latest.UserID != conn.UserID || latest.User == nil || latest.User.ID != conn.UserID || latest.Key != conn.Key {
		return service.NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, "websocket authorization is no longer valid, please reconnect", nil)
	}
	if !latest.User.IsActive() {
		return service.NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, "User account is not active", nil)
	}
	if !latest.IsActive() || latest.IsExpired() {
		return service.NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, "API key is no longer active, please reconnect", nil)
	}
	k.mu.Lock()
	k.admittedTurn, k.admittedKey = turn, latest
	k.mu.Unlock()
	return nil
}

func (k *openAIWSTurnBillingAPIKeys) takeAdmittedKey(turn int) *service.APIKey {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.admittedTurn != turn {
		return nil
	}
	latest := k.admittedKey
	k.admittedTurn, k.admittedKey = 0, nil
	return latest
}

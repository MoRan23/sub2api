package repository

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

const codexStateCancelChannel = "openai:codex:state:cancel:v1"

func (r *openAICodexStateRepository) redisAvailable() error {
	if r == nil || r.rdb == nil {
		return errors.New("Codex turn-state coordination unavailable")
	}
	return nil
}

// The account-wide lease intentionally excludes the model and configuration
// generation: two instances may never collect different models simultaneously
// using the same credential owner. The network deadline must be shorter than ttl.
func (r *openAICodexStateRepository) AcquireCollector(ctx context.Context, ownerID int64, lockID string, ttl time.Duration) (bool, error) {
	if err := r.redisAvailable(); err != nil {
		return false, err
	}
	key, err := codexStateCollectorKey(ownerID)
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(lockID) == "" || ttl <= 0 {
		return false, errors.New("invalid Codex turn-state collector lease")
	}
	return r.rdb.SetNX(ctx, key, lockID, ttl).Result()
}

func (r *openAICodexStateRepository) ReleaseCollector(ctx context.Context, ownerID int64, lockID string) error {
	if err := r.redisAvailable(); err != nil {
		return err
	}
	key, err := codexStateCollectorKey(ownerID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(lockID) == "" {
		return errors.New("invalid Codex turn-state collector owner")
	}
	return leaderLockReleaseScript.Run(ctx, r.rdb, []string{key}, lockID).Err()
}

// Notifications only carry the scope. They can cancel work eagerly, but losing a
// notification cannot permit stale publication: PostgreSQL generation/version
// CAS remains the publication authority.
func (r *openAICodexStateRepository) PublishCancel(ctx context.Context, key service.CodexTurnStateKey) error {
	if err := r.redisAvailable(); err != nil {
		return err
	}
	if err := validateCodexStateKey(key); err != nil {
		return err
	}
	payload, err := json.Marshal(key)
	if err != nil {
		return err
	}
	return r.rdb.Publish(ctx, codexStateCancelChannel, payload).Err()
}

func (r *openAICodexStateRepository) SubscribeCancels(ctx context.Context, handle func(service.CodexTurnStateKey)) error {
	if err := r.redisAvailable(); err != nil {
		return err
	}
	if handle == nil {
		return errors.New("nil Codex turn-state cancellation handler")
	}
	subscription := r.rdb.Subscribe(ctx, codexStateCancelChannel)
	defer func() { _ = subscription.Close() }()
	if _, err := subscription.Receive(ctx); err != nil {
		return err
	}
	for {
		// ReceiveMessage honors context cancellation once the subscription closes;
		// use the channel so cancellation is prompt even on an idle Redis socket.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case message, ok := <-subscription.Channel():
			if !ok {
				return errors.New("Codex turn-state cancellation subscription closed")
			}
			if len(message.Payload) > 4096 {
				continue
			}
			var key service.CodexTurnStateKey
			if json.Unmarshal([]byte(message.Payload), &key) == nil && validateCodexStateKey(key) == nil {
				handle(key)
			}
		}
	}
}

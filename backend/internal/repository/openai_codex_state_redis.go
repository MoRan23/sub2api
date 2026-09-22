package repository

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
)

const codexStateCancelChannel = "openai:codex:state:cancel:v2"
const codexStateActivationChannel = "openai:codex:state:activate:v2"

type codexStateActivation struct {
	OwnerAccountID int64  `json:"owner_account_id"`
	OSFamily       string `json:"os_family"`
	Generation     string `json:"generation"`
}

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

// Notifications carry the scope and optionally one exact collection attempt.
// An empty attempt remains the configuration-level cancellation contract.
// They can cancel work eagerly, but losing a
// notification cannot permit stale publication: PostgreSQL generation/version
// CAS remains the publication authority.
func (r *openAICodexStateRepository) PublishCancel(ctx context.Context, key service.CodexTurnStateKey) error {
	if err := r.redisAvailable(); err != nil {
		return err
	}
	if err := validateCodexStateCancelKey(key); err != nil {
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
			if json.Unmarshal([]byte(message.Payload), &key) == nil && validateCodexStateCancelKey(key) == nil {
				handle(key)
			}
		}
	}
}

func validateCodexStateCancelKey(key service.CodexTurnStateKey) error {
	if err := validateCodexStateKey(key); err != nil {
		return err
	}
	if key.CollectorAttemptID != "" {
		if _, err := uuid.Parse(key.CollectorAttemptID); err != nil {
			return errors.New("invalid Codex turn-state collector attempt")
		}
	}
	return nil
}

// Activation messages carry only the current configuration scope. Every
// subscriber revalidates PostgreSQL and its own safe observation before demand
// can be created. Neither observations nor credentials are broadcast.
func (r *openAICodexStateRepository) PublishOSActivation(ctx context.Context, ownerID int64, osFamily, generation string) error {
	if err := r.redisAvailable(); err != nil {
		return err
	}
	if ownerID <= 0 || osFamily == "" || service.NormalizeOpenAIOSFamily(osFamily) != osFamily || strings.TrimSpace(generation) == "" {
		return errors.New("invalid Codex turn-state activation scope")
	}
	payload, err := json.Marshal(codexStateActivation{OwnerAccountID: ownerID, OSFamily: osFamily, Generation: generation})
	if err != nil {
		return err
	}
	return r.rdb.Publish(ctx, codexStateActivationChannel, payload).Err()
}

func (r *openAICodexStateRepository) SubscribeOSActivations(ctx context.Context, handle func(int64, string, string)) error {
	if err := r.redisAvailable(); err != nil {
		return err
	}
	if handle == nil {
		return errors.New("nil Codex turn-state activation handler")
	}
	subscription := r.rdb.Subscribe(ctx, codexStateActivationChannel)
	defer func() { _ = subscription.Close() }()
	if _, err := subscription.Receive(ctx); err != nil {
		return err
	}
	messages := subscription.Channel()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case message, ok := <-messages:
			if !ok {
				return errors.New("Codex turn-state activation subscription closed")
			}
			if len(message.Payload) > 4096 {
				continue
			}
			var activation codexStateActivation
			if json.Unmarshal([]byte(message.Payload), &activation) == nil && activation.OwnerAccountID > 0 && activation.OSFamily != "" && service.NormalizeOpenAIOSFamily(activation.OSFamily) == activation.OSFamily && strings.TrimSpace(activation.Generation) != "" {
				handle(activation.OwnerAccountID, activation.OSFamily, activation.Generation)
			}
		}
	}
}

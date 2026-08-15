package repository

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const openAICodexTurnStateOriginKeyPrefix = "openai:codex:turn-state-origin:v1:"

func openAICodexTurnStateOriginRedisKey(mappingKey string) (string, error) {
	mappingKey = strings.TrimSpace(mappingKey)
	decoded, err := hex.DecodeString(mappingKey)
	if err != nil || len(decoded) != 32 || len(mappingKey) != 64 || mappingKey != strings.ToLower(mappingKey) {
		return "", errors.New("invalid OpenAI Codex turn-state mapping key")
	}
	return openAICodexTurnStateOriginKeyPrefix + mappingKey, nil
}

func (c *gatewayCache) GetOpenAICodexTurnStateOrigin(ctx context.Context, mappingKey string) (service.OpenAICodexTurnStateOrigin, error) {
	if c == nil || c.rdb == nil {
		return service.OpenAICodexTurnStateOrigin{}, errors.New("gateway cache unavailable")
	}
	key, err := openAICodexTurnStateOriginRedisKey(mappingKey)
	if err != nil {
		return service.OpenAICodexTurnStateOrigin{}, err
	}
	payload, err := c.rdb.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return service.OpenAICodexTurnStateOrigin{}, service.ErrOpenAICodexTurnStateOriginNotFound
	}
	if err != nil {
		return service.OpenAICodexTurnStateOrigin{}, err
	}
	var origin service.OpenAICodexTurnStateOrigin
	if err := json.Unmarshal(payload, &origin); err != nil {
		return service.OpenAICodexTurnStateOrigin{}, err
	}
	if strings.TrimSpace(origin.CredentialOwnerNamespace) == "" || strings.TrimSpace(origin.TurnIdentityDigest) == "" {
		return service.OpenAICodexTurnStateOrigin{}, errors.New("invalid OpenAI Codex turn-state origin")
	}
	return origin, nil
}

func (c *gatewayCache) SetOpenAICodexTurnStateOrigin(ctx context.Context, mappingKey string, origin service.OpenAICodexTurnStateOrigin, ttl time.Duration) error {
	if c == nil || c.rdb == nil {
		return errors.New("gateway cache unavailable")
	}
	key, err := openAICodexTurnStateOriginRedisKey(mappingKey)
	if err != nil {
		return err
	}
	if strings.TrimSpace(origin.CredentialOwnerNamespace) == "" || strings.TrimSpace(origin.TurnIdentityDigest) == "" {
		return errors.New("invalid OpenAI Codex turn-state origin")
	}
	if ttl <= 0 {
		return errors.New("invalid OpenAI Codex turn-state TTL")
	}
	payload, err := json.Marshal(origin)
	if err != nil {
		return err
	}
	return c.rdb.Set(ctx, key, payload, ttl).Err()
}

func (c *gatewayCache) DeleteOpenAICodexTurnStateOrigin(ctx context.Context, mappingKey string) error {
	if c == nil || c.rdb == nil {
		return errors.New("gateway cache unavailable")
	}
	key, err := openAICodexTurnStateOriginRedisKey(mappingKey)
	if err != nil {
		return err
	}
	return c.rdb.Del(ctx, key).Err()
}

var _ service.OpenAICodexTurnStateOriginStore = (*gatewayCache)(nil)

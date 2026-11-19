package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

func (c *gatewayCache) SetOpenAIResponseEffort(ctx context.Context, groupID, userID int64, responseID string, state service.OpenAIResponseEffortState, ttl time.Duration) error {
	value, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return c.rdb.Set(ctx, fmt.Sprintf("openai:response-effort:v1:%d:%d:%s", groupID, userID, responseID), value, ttl).Err()
}
func (c *gatewayCache) GetOpenAIResponseEffort(ctx context.Context, groupID, userID int64, responseID string) (service.OpenAIResponseEffortState, bool, error) {
	var state service.OpenAIResponseEffortState
	value, err := c.rdb.Get(ctx, fmt.Sprintf("openai:response-effort:v1:%d:%d:%s", groupID, userID, responseID)).Bytes()
	if errors.Is(err, redis.Nil) {
		return state, false, nil
	}
	if err != nil {
		return state, false, err
	}
	if len(value) > 4096 || json.Unmarshal(value, &state) != nil {
		return state, false, nil
	}
	return state, true, nil
}

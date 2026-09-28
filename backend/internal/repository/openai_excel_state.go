package repository

import (
	"context"
	"errors"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const excelStatePrefix = "openai:excel:state:v1:"

var putExcelStateScript = redis.NewScript(`
local now = tonumber(redis.call('TIME')[1])
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now)
redis.call('SET', KEYS[2], ARGV[1], 'EX', ARGV[2])
redis.call('ZADD', KEYS[1], now + tonumber(ARGV[2]), KEYS[2])
local excess = redis.call('ZCARD', KEYS[1]) - tonumber(ARGV[3])
if excess > 0 then
  local old = redis.call('ZRANGE', KEYS[1], 0, excess)
  for _, key in ipairs(old) do
    if key ~= KEYS[2] and excess > 0 then
      redis.call('DEL', key)
      redis.call('ZREM', KEYS[1], key)
      excess = excess - 1
    end
  end
end
redis.call('EXPIRE', KEYS[1], ARGV[2])
return 1
`)

func (c *gatewayCache) PutOpenAIExcelState(ctx context.Context, scope, key, ciphertext string, ttl time.Duration, limit int) error {
	if c == nil || c.rdb == nil || len(scope) != 64 || len(key) != 64 || ttl <= 0 || limit <= 0 || limit > 4096 {
		return service.ErrOpenAIExcelStateUnavailable
	}
	prefix := excelStatePrefix + "{" + scope + "}:"
	return putExcelStateScript.Run(ctx, c.rdb, []string{prefix + "index", prefix + key}, ciphertext, max(1, int64(ttl/time.Second)), limit).Err()
}

func (c *gatewayCache) GetOpenAIExcelState(ctx context.Context, scope, key string) (string, error) {
	if c == nil || c.rdb == nil {
		return "", service.ErrOpenAIExcelStateUnavailable
	}
	value, err := c.rdb.Get(ctx, excelStatePrefix+"{"+scope+"}:"+key).Result()
	if errors.Is(err, redis.Nil) {
		return "", service.ErrOpenAIExcelStateNotFound
	}
	return value, err
}

var _ service.OpenAIExcelStateBackend = (*gatewayCache)(nil)

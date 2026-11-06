package runtimeobs

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

const RedisTTL = 70 * time.Minute
const MaxInstances = 128
const redisPrefix = "ops:runtime:memory:"

// RedisStore uses bounded lists and a bounded process index, never the database.
type RedisStore struct{ client *redis.Client }

func NewRedisStore(client *redis.Client) *RedisStore { return &RedisStore{client: client} }

var saveScript = redis.NewScript(`
local expired = redis.call('ZRANGEBYSCORE', KEYS[1], '-inf', ARGV[1])
for _, id in ipairs(expired) do
 redis.call('DEL', ARGV[7] .. id .. ':history', ARGV[7] .. id .. ':instance')
 redis.call('ZREM', KEYS[1], id)
end
if not redis.call('ZSCORE', KEYS[1], ARGV[3]) and redis.call('ZCARD', KEYS[1]) >= 128 then
 redis.call('SET', ARGV[7] .. 'truncated', '1', 'EX', ARGV[6])
 return 2
end
redis.call('ZADD', KEYS[1], ARGV[2], ARGV[3])
redis.call('SET', KEYS[2], ARGV[4], 'EX', ARGV[6])
redis.call('RPUSH', KEYS[3], ARGV[5])
redis.call('LTRIM', KEYS[3], -360, -1)
redis.call('EXPIRE', KEYS[3], ARGV[6])
redis.call('EXPIRE', KEYS[1], ARGV[6])
return 1
`)

// Truncated reports capacity pressure across the cluster. Existing process
// histories are retained until expiry; new processes keep their local ring.
func (s *RedisStore) Truncated(ctx context.Context) (bool, error) {
	n, err := s.client.Exists(ctx, redisPrefix+"truncated").Result()
	return n != 0, err
}

func (s *RedisStore) Save(ctx context.Context, inst Instance, sample Sample) error {
	if s.client == nil {
		return fmt.Errorf("runtime memory Redis is unavailable")
	}
	if _, err := uuid.Parse(inst.InstanceID); err != nil {
		return err
	}
	meta, err := json.Marshal(inst)
	if err != nil {
		return err
	}
	data, err := json.Marshal(sample)
	if err != nil {
		return err
	}
	return saveScript.Run(ctx, s.client, []string{redisPrefix + "instances", redisPrefix + inst.InstanceID + ":instance", redisPrefix + inst.InstanceID + ":history"}, inst.LastSeenAt.Add(-RedisTTL).Unix(), inst.LastSeenAt.Unix(), inst.InstanceID, string(meta), string(data), int(RedisTTL.Seconds()), redisPrefix).Err()
}
func (s *RedisStore) Read(ctx context.Context, selected string) ([]Instance, []Sample, error) {
	instances := make([]Instance, 0)
	history := make([]Sample, 0)
	if s.client == nil {
		return instances, history, fmt.Errorf("runtime memory Redis is unavailable")
	}
	if _, err := uuid.Parse(selected); err != nil {
		return instances, history, err
	}
	ids, err := s.client.ZRevRange(ctx, redisPrefix+"instances", 0, MaxInstances-1).Result()
	if err != nil {
		return instances, history, err
	}
	pipe := s.client.Pipeline()
	commands := make([]*redis.StringCmd, 0, len(ids))
	for _, id := range ids {
		if _, err := uuid.Parse(id); err == nil {
			commands = append(commands, pipe.Get(ctx, redisPrefix+id+":instance"))
		}
	}
	h := pipe.LRange(ctx, redisPrefix+selected+":history", -HistoryLimit, -1)
	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		return instances, history, err
	}
	for _, command := range commands {
		if raw, err := command.Result(); err == nil {
			var inst Instance
			if json.Unmarshal([]byte(raw), &inst) == nil {
				instances = append(instances, inst)
			}
		}
	}
	for _, raw := range h.Val() {
		var sample Sample
		if json.Unmarshal([]byte(raw), &sample) == nil {
			history = append(history, sample)
		}
	}
	return instances, history, nil
}

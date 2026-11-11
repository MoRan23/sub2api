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

const publicCodexCatalogRedisKey = "codex:public-model-catalog:v1"

type publicCodexCatalogStore struct{ client *redis.Client }

func NewPublicCodexCatalogStore(client *redis.Client) service.PublicCodexCatalogStore {
	return &publicCodexCatalogStore{client: client}
}

func (s *publicCodexCatalogStore) Load(ctx context.Context) (*service.PublicCodexCatalogSnapshot, error) {
	data, err := s.client.HGet(ctx, publicCodexCatalogRedisKey, "snapshot").Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// The envelope includes base64 overhead in addition to the 1 MiB body.
	if len(data) > 2<<20 {
		return nil, fmt.Errorf("cached public catalog exceeds size limit")
	}
	var snapshot service.PublicCodexCatalogSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return nil, err
	}
	return &snapshot, nil
}

var savePublicCodexCatalogScript = redis.NewScript(`
local previous = redis.call('HGET', KEYS[1], 'snapshot') or ''
if previous ~= ARGV[3] then return 0 end
redis.call('HSET', KEYS[1], 'checked_at_ms', ARGV[1], 'snapshot', ARGV[2])
return 1
`)

func (s *publicCodexCatalogStore) Save(ctx context.Context, snapshot *service.PublicCodexCatalogSnapshot) error {
	if err := service.ValidatePublicCodexCatalogSnapshot(snapshot, time.Now()); err != nil {
		return fmt.Errorf("invalid public catalog snapshot: %w", err)
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	// Compare the validated snapshot itself at full precision. The advisory
	// millisecond index can be corrupt/future-dated and must not prevent healing.
	// CAS protects a newer concurrent observation between the read and write.
	for range 8 {
		previous, err := s.client.HGet(ctx, publicCodexCatalogRedisKey, "snapshot").Bytes()
		if err != nil && !errors.Is(err, redis.Nil) {
			return err
		}
		writeData, observed := data, snapshot.CheckedAt
		if len(previous) <= 2<<20 {
			var cached service.PublicCodexCatalogSnapshot
			if json.Unmarshal(previous, &cached) == nil && service.ValidatePublicCodexCatalogSnapshot(&cached, time.Now()) == nil && cached.CheckedAt.After(observed) {
				writeData, observed = previous, cached.CheckedAt
			}
		}
		// One bounded key intentionally has no TTL: restarts keep last good data.
		written, err := savePublicCodexCatalogScript.Run(ctx, s.client, []string{publicCodexCatalogRedisKey}, observed.UnixMilli(), writeData, previous).Int()
		if err != nil {
			return err
		}
		if written == 1 {
			return nil
		}
	}
	return errors.New("public catalog cache changed during concurrent saves")
}
